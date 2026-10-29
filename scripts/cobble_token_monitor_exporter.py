#!/usr/bin/env python3
"""Collect Cobble model-call metadata over SSH and push to Token Monitor.

Only metadata is transferred: call id, timestamp, model, token usage,
duration, attempts, and optional error text. Prompts and responses stay on
the remote Cobble host.
"""

from __future__ import annotations

import argparse
import copy
import datetime as dt
import json
import os
import socket
import sys
import tempfile
import time
from pathlib import Path
from typing import Any, Iterable

import token_monitor_ua_exporter as tm


SCRIPT_DIR = Path(__file__).resolve().parent
REPO_ROOT = SCRIPT_DIR.parent
DEFAULT_CONFIG_PATH = SCRIPT_DIR / "cobble_token_monitor_exporter.json"
DEFAULT_STATE_FILE = (
    REPO_ROOT.parent
    / "wb2api-panel-windows-amd64"
    / "data"
    / "cobble-token-monitor-state.json"
)
DEFAULT_PRICING_BINARY = (
    REPO_ROOT.parent
    / "Token-Monitor"
    / "resources"
    / "app.asar.unpacked"
    / "node_modules"
    / "@tokscale"
    / "cli-win32-x64-msvc"
    / "bin"
    / "tokscale.exe"
)
STATE_VERSION = 1

DEFAULT_CONFIG: dict[str, Any] = {
    "ssh_host": "10.28.0.132",
    "ssh_port": 22,
    "ssh_user": "xue",
    "ssh_password": "",
    "ssh_password_env": "COBBLE_SSH_PASSWORD",
    "ssh_key_file": "",
    "ssh_timeout_seconds": 10,
    "remote_globs": [
        "/home/xue/Cobble/data/model-calls/model-calls.jsonl",
        "/home/xue/Cobble/runs/*/data/model-calls/model-calls.jsonl",
    ],
    "hub_url": "http://127.0.0.1:17321",
    "state_file": str(DEFAULT_STATE_FILE),
    "secret_file": r"%APPDATA%\Token Monitor\credentials.json",
    "device_id": "cobble-vm",
    "hostname": "xue",
    "client_id": "cobble",
    "interval_seconds": 60,
    "utc_offset_minutes": 480,
    "time_zone_name": "Asia/Shanghai",
    "history_daily_max_days": 370,
    "payload_max_bytes": 900_000,
    "http_timeout_seconds": 15,
    "retry_attempts": 5,
    "pricing_binary": str(DEFAULT_PRICING_BINARY),
    "pricing_cache_ttl_seconds": 86400,
    "pricing_timeout_seconds": 12,
    "pricing_min_model_tokens": 1000,
    "pricing_max_lookups_per_run": 8,
    "pricing_concurrency": 6,
}

REMOTE_COLLECTOR = r"""
import glob
import json
import os
import sys

patterns = json.loads(sys.argv[1])
records = []
files = []
errors = []
for pattern in patterns:
    for path in sorted(glob.glob(pattern)):
        if not os.path.isfile(path):
            continue
        info = {"path": path, "lines": 0, "bytes": os.path.getsize(path)}
        try:
            handle = open(path, "r", encoding="utf-8", errors="replace")
        except OSError as exc:
            errors.append({"path": path, "error": str(exc)})
            continue
        with handle:
            for line_no, line in enumerate(handle, 1):
                info["lines"] += 1
                line = line.strip()
                if not line:
                    continue
                try:
                    obj = json.loads(line)
                except Exception as exc:
                    errors.append({"path": path, "line": line_no, "error": str(exc)})
                    continue
                usage = obj.get("usage") or {}
                if not isinstance(usage, dict):
                    usage = {}
                if "/runs/" in path:
                    source = path.split("/runs/", 1)[1].split("/data/model-calls/", 1)[0]
                elif path.endswith("/data/model-calls/model-calls.jsonl"):
                    source = "main"
                else:
                    source = os.path.dirname(path)
                records.append({
                    "id": str(obj.get("call_id") or (path + ":" + str(line_no))),
                    "timestamp": str(obj.get("timestamp") or ""),
                    "model": str(obj.get("model") or ""),
                    "prompt_tokens": int(usage.get("prompt_tokens") or 0),
                    "completion_tokens": int(usage.get("completion_tokens") or 0),
                    "total_tokens": int(usage.get("total_tokens") or 0),
                    "duration_ms": int(obj.get("duration_ms") or 0),
                    "attempts": int(obj.get("attempts") or 0),
                    "error": str(obj.get("error") or ""),
                    "source": source,
                })
        files.append(info)
print(json.dumps({"records": records, "files": files, "errors": errors}, ensure_ascii=True))
"""


class ConfigError(RuntimeError):
    pass


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Collect Cobble model-call token metadata over SSH and push to Token Monitor."
    )
    parser.add_argument("--config", type=Path)
    parser.add_argument("--state-file", type=Path)
    parser.add_argument("--ssh-host")
    parser.add_argument("--ssh-port", type=int)
    parser.add_argument("--ssh-user")
    parser.add_argument("--ssh-password")
    parser.add_argument("--ssh-key-file", type=Path)
    parser.add_argument("--hub-url")
    parser.add_argument("--secret-file", type=Path)
    parser.add_argument("--device-id")
    parser.add_argument("--interval", type=int)
    parser.add_argument("--once", action="store_true")
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument("--print-payload", action="store_true")
    parser.add_argument("--rebuild", action="store_true")
    parser.add_argument("--reset-state", action="store_true")
    parser.add_argument("--no-pricing", action="store_true")
    parser.add_argument("--verbose", action="store_true")
    return parser.parse_args(argv)


def deep_merge(base: dict[str, Any], override: dict[str, Any]) -> dict[str, Any]:
    result = copy.deepcopy(base)
    for key, value in override.items():
        if isinstance(value, dict) and isinstance(result.get(key), dict):
            result[key] = deep_merge(result[key], value)
        else:
            result[key] = copy.deepcopy(value)
    return result


def resolve_path(value: Any, base: Path) -> Path:
    expanded = os.path.expandvars(os.path.expanduser(str(value or "")))
    path = Path(expanded)
    return path if path.is_absolute() else (base / path).resolve()


def resolve_optional_path(value: Any, base: Path) -> Path | None:
    if value in (None, ""):
        return None
    return resolve_path(value, base)


def load_config(args: argparse.Namespace) -> dict[str, Any]:
    config_path = args.config
    if config_path is None and DEFAULT_CONFIG_PATH.exists():
        config_path = DEFAULT_CONFIG_PATH
    if config_path is not None:
        config_path = config_path.expanduser().resolve()
        if not config_path.exists():
            raise ConfigError(f"config file does not exist: {config_path}")
        try:
            raw = json.loads(config_path.read_text(encoding="utf-8-sig"))
        except (OSError, json.JSONDecodeError) as exc:
            raise ConfigError(f"read config {config_path}: {exc}") from exc
        if not isinstance(raw, dict):
            raise ConfigError("config root must be a JSON object")
        base_dir = config_path.parent
        config = deep_merge(DEFAULT_CONFIG, raw)
    else:
        base_dir = SCRIPT_DIR
        config = copy.deepcopy(DEFAULT_CONFIG)

    cli_base = Path.cwd()
    if args.state_file:
        config["state_file"] = str(resolve_path(args.state_file, cli_base))
    if args.ssh_host:
        config["ssh_host"] = args.ssh_host
    if args.ssh_port is not None:
        config["ssh_port"] = args.ssh_port
    if args.ssh_user:
        config["ssh_user"] = args.ssh_user
    if args.ssh_password is not None:
        config["ssh_password"] = args.ssh_password
    if args.ssh_key_file:
        config["ssh_key_file"] = str(resolve_path(args.ssh_key_file, cli_base))
    if args.hub_url:
        config["hub_url"] = args.hub_url
    if args.secret_file:
        config["secret_file"] = str(resolve_path(args.secret_file, cli_base))
    if args.device_id:
        config["device_id"] = args.device_id
    if args.interval is not None:
        config["interval_seconds"] = args.interval
    if args.no_pricing:
        config["pricing_binary"] = ""

    config["state_file"] = str(resolve_path(config["state_file"], base_dir))
    config["secret_file"] = resolve_optional_path(config.get("secret_file"), base_dir)
    config["ssh_key_file"] = resolve_optional_path(config.get("ssh_key_file"), base_dir)
    config["pricing_binary"] = resolve_optional_path(config.get("pricing_binary"), base_dir)
    config["hub_url"] = str(config.get("hub_url") or DEFAULT_CONFIG["hub_url"]).rstrip("/")
    config["device_id"] = tm.sanitize_client_name(config.get("device_id")) or "cobble-vm"
    config["client_id"] = tm.sanitize_client_name(config.get("client_id")) or "cobble"
    config["ssh_host"] = str(config.get("ssh_host") or "").strip()
    config["ssh_user"] = str(config.get("ssh_user") or "").strip()
    config["ssh_port"] = max(1, int(config.get("ssh_port") or 22))
    config["ssh_timeout_seconds"] = max(1, int(config.get("ssh_timeout_seconds") or 10))
    config["interval_seconds"] = max(1, int(config.get("interval_seconds") or 60))
    config["utc_offset_minutes"] = int(config.get("utc_offset_minutes") or 0)
    config["time_zone_name"] = str(config.get("time_zone_name") or "UTC+08:00")
    config["history_daily_max_days"] = max(1, int(config.get("history_daily_max_days") or 370))
    config["payload_max_bytes"] = max(1024, int(config.get("payload_max_bytes") or 900_000))
    config["http_timeout_seconds"] = max(1, int(config.get("http_timeout_seconds") or 15))
    config["retry_attempts"] = max(1, int(config.get("retry_attempts") or 5))
    config["pricing_cache_ttl_seconds"] = max(
        0, int(config.get("pricing_cache_ttl_seconds") or 0)
    )
    config["pricing_timeout_seconds"] = max(
        1, int(config.get("pricing_timeout_seconds") or 12)
    )
    config["pricing_min_model_tokens"] = max(
        0, int(config.get("pricing_min_model_tokens") or 0)
    )
    config["pricing_max_lookups_per_run"] = max(
        0, int(config.get("pricing_max_lookups_per_run") or 0)
    )
    config["pricing_concurrency"] = max(
        1, int(config.get("pricing_concurrency") or 6)
    )
    if not config["ssh_host"]:
        raise ConfigError("ssh_host is required")
    if not config["ssh_user"]:
        raise ConfigError("ssh_user is required")
    if not isinstance(config.get("remote_globs"), list) or not config["remote_globs"]:
        raise ConfigError("remote_globs must be a non-empty JSON array")
    return config


def ssh_password(config: dict[str, Any]) -> str:
    value = str(config.get("ssh_password") or "")
    if value:
        return value
    env_name = str(config.get("ssh_password_env") or "COBBLE_SSH_PASSWORD")
    return os.environ.get(env_name, "").strip()


def connect_ssh(config: dict[str, Any]) -> Any:
    try:
        import paramiko
    except ImportError as exc:
        raise ConfigError("paramiko is required for SSH collection") from exc
    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    kwargs: dict[str, Any] = {
        "hostname": config["ssh_host"],
        "port": int(config["ssh_port"]),
        "username": config["ssh_user"],
        "timeout": int(config["ssh_timeout_seconds"]),
        "banner_timeout": int(config["ssh_timeout_seconds"]),
        "auth_timeout": int(config["ssh_timeout_seconds"]),
        "look_for_keys": not bool(config.get("ssh_key_file")),
        "allow_agent": False,
    }
    if config.get("ssh_key_file"):
        kwargs["key_filename"] = str(config["ssh_key_file"])
        password = ssh_password(config)
        if password:
            kwargs["passphrase"] = password
    else:
        password = ssh_password(config)
        if not password:
            raise ConfigError(
                f"SSH password is empty; set config.ssh_password or env "
                f"{config.get('ssh_password_env') or 'COBBLE_SSH_PASSWORD'}"
            )
        kwargs["password"] = password
    client.connect(**kwargs)
    return client


def collect_remote(config: dict[str, Any]) -> dict[str, Any]:
    client = connect_ssh(config)
    try:
        command = "python3 - " + json.dumps(json.dumps(config["remote_globs"]))
        stdin, stdout, stderr = client.exec_command(command, timeout=60)
        stdin.write(REMOTE_COLLECTOR)
        stdin.channel.shutdown_write()
        output = stdout.read().decode("utf-8", "replace")
        error = stderr.read().decode("utf-8", "replace")
    finally:
        client.close()
    if error.strip():
        print(f"[cobble-export] remote stderr: {error.strip()[:1000]}", file=sys.stderr)
    try:
        payload = json.loads(output)
    except json.JSONDecodeError as exc:
        raise ConfigError(f"invalid remote collector output: {exc}") from exc
    if not isinstance(payload, dict):
        raise ConfigError("remote collector output must be a JSON object")
    return payload


def normalize_model(value: Any) -> str:
    model = str(value or "").strip().lower()
    if ":" in model:
        stripped = model.split(":", 1)[1].strip()
        if stripped:
            model = stripped
    return model or "unknown"


def add_cobble_record(
    period: dict[str, Any],
    record: dict[str, Any],
    client: str,
) -> bool:
    prompt = max(0, int(record.get("prompt_tokens") or 0))
    completion = max(0, int(record.get("completion_tokens") or 0))
    total = max(0, int(record.get("total_tokens") or 0))
    if total <= 0:
        total = prompt + completion
    if total <= 0:
        return False
    model = normalize_model(record.get("model"))
    duration_ms = max(0, int(record.get("duration_ms") or 0))

    # Cobble does not expose cache hit/miss. Treat prompt tokens as normal
    # input for pricing; Token Monitor will show zero cache hit for this source.
    period["totalTokens"] += total
    period["outputTokens"] += min(total, completion)
    period["timedTokens"] += total
    period["timedOutputTokens"] += min(total, completion)
    period["timedDurationMs"] += duration_ms
    period["messageCount"] += 1

    tm.add_number_map(period["clients"], {client: total})
    tm.add_number_map(period["clientMessages"], {client: 1})
    tm.add_number_map(period["clientOutputs"], {client: min(total, completion)})
    tm.add_number_map(period["models"], {model: total})
    tm.add_number_map(period["modelOutputs"], {model: min(total, completion)})
    tm.add_number_map(period["clientModels"].setdefault(client, {}), {model: total})
    return True


def empty_state() -> dict[str, Any]:
    return {
        "version": STATE_VERSION,
        "seen": {},
        "days": {},
        "pricing": {},
        "updatedAt": "",
    }


def load_state(path: Path) -> dict[str, Any]:
    if not path.exists():
        return empty_state()
    try:
        state = json.loads(path.read_text(encoding="utf-8-sig"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ConfigError(f"read state {path}: {exc}") from exc
    if not isinstance(state, dict) or state.get("version") != STATE_VERSION:
        raise ConfigError(f"unsupported state version in {path}")
    if not isinstance(state.get("seen"), dict):
        state["seen"] = {}
    if not isinstance(state.get("days"), dict):
        state["days"] = {}
    if not isinstance(state.get("pricing"), dict):
        state["pricing"] = {}
    return state


def save_state(path: Path, state: dict[str, Any], now: dt.datetime) -> None:
    state["version"] = STATE_VERSION
    state["updatedAt"] = tm.iso_z(now)
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temp_name = tempfile.mkstemp(
        prefix=path.name + ".",
        suffix=".tmp",
        dir=str(path.parent),
    )
    try:
        with os.fdopen(fd, "w", encoding="utf-8", newline="\n") as handle:
            json.dump(state, handle, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temp_name, path)
    finally:
        try:
            os.unlink(temp_name)
        except FileNotFoundError:
            pass


def ingest_records(
    state: dict[str, Any],
    records: Iterable[dict[str, Any]],
    config: dict[str, Any],
) -> dict[str, int]:
    stats = {"records": 0, "new": 0, "duplicates": 0, "ignored": 0, "bad_time": 0}
    seen = state["seen"]
    days = state["days"]
    client = config["client_id"]
    offset = int(config["utc_offset_minutes"])
    for record in records:
        if not isinstance(record, dict):
            stats["ignored"] += 1
            continue
        stats["records"] += 1
        call_id = str(record.get("id") or "").strip()
        if not call_id:
            stats["ignored"] += 1
            continue
        if call_id in seen:
            stats["duplicates"] += 1
            continue
        instant = tm.parse_log_time(record.get("timestamp"), offset)
        if instant is None:
            stats["bad_time"] += 1
            continue
        day = tm.local_date_key(instant, offset)
        period = days.setdefault(day, tm.empty_period())
        if not add_cobble_record(period, record, client):
            stats["ignored"] += 1
            continue
        seen[call_id] = 1
        stats["new"] += 1
    return stats


def collect_model_totals(state: dict[str, Any]) -> dict[str, int]:
    totals: dict[str, int] = {}
    for period in state["days"].values():
        for model, tokens in period.get("models", {}).items():
            totals[str(model)] = totals.get(str(model), 0) + int(tokens)
    return totals


def aggregate_days(
    days: dict[str, dict[str, Any]],
    predicate: Any = None,
) -> dict[str, Any]:
    result = tm.empty_period()
    for day, period in days.items():
        if predicate is not None and not predicate(day):
            continue
        tm.add_period(result, period)
    return result


def build_payload(
    state: dict[str, Any],
    config: dict[str, Any],
    now: dt.datetime,
    rates_by_model: dict[str, dict[str, float]] | None = None,
) -> dict[str, Any]:
    offset = int(config["utc_offset_minutes"])
    windows = tm.period_windows(now, offset, str(config["time_zone_name"]))
    days = state["days"]
    today_key = windows["today"]["key"]
    month_prefix = windows["month"]["key"]
    today_period = days.get(today_key, tm.empty_period())
    month_period = aggregate_days(days, lambda day: day.startswith(month_prefix))
    all_time = aggregate_days(days)
    clients = sorted(all_time.get("clients", {}))
    payload = {
        "deviceId": config["device_id"],
        "hostname": str(config.get("hostname") or config.get("ssh_host") or socket.gethostname()),
        "platform": "linux-x86_64",
        "agentVersion": "1.0.0",
        "agentRuntime": "ssh-collector",
        "updatedAt": tm.iso_z(now),
        "trackedClients": clients,
        "clientStatus": {client: "active" for client in clients},
        "projectsEnabled": False,
        "historyAvailable": True,
        "history": tm.build_history(
            days,
            windows,
            int(config["history_daily_max_days"]),
            rates_by_model,
        ),
        "periodWindows": windows,
        "periods": {
            "today": tm.period_to_payload(today_period, rates_by_model),
            "month": tm.period_to_payload(month_period, rates_by_model),
            "allTime": tm.period_to_payload(all_time, rates_by_model),
        },
    }
    return tm.fit_payload(payload, int(config["payload_max_bytes"]))


def run_cycle(config: dict[str, Any], args: argparse.Namespace) -> int:
    state_path = Path(config["state_file"])
    if args.rebuild:
        previous = load_state(state_path) if state_path.exists() else empty_state()
        state = empty_state()
        state["pricing"] = previous.get("pricing", {})
    else:
        state = load_state(state_path)

    remote = collect_remote(config)
    stats = ingest_records(state, remote.get("records") or [], config)
    now = dt.datetime.now(dt.timezone.utc)
    model_totals = collect_model_totals(state)
    models_by_volume = [
        model
        for model, _ in sorted(model_totals.items(), key=lambda item: (-item[1], item[0]))
    ]
    provider = tm.PricingProvider(
        config,
        state.setdefault("pricing", {}),
        now,
        log_prefix="[cobble-export]",
    )
    rates_by_model = provider.resolve_models(models_by_volume, model_totals)
    payload = build_payload(state, config, now, rates_by_model)

    commit = not (args.dry_run or args.print_payload)
    if commit:
        save_state(state_path, state, now)

    files = remote.get("files") or []
    errors = remote.get("errors") or []
    totals = payload["periods"]["allTime"]
    if args.print_payload:
        print(json.dumps(payload, ensure_ascii=False, indent=2))
    else:
        print(
            "[cobble-export] "
            f"files={len(files)} remote_records={stats['records']} "
            f"new={stats['new']} duplicates={stats['duplicates']} "
            f"ignored={stats['ignored']} bad_time={stats['bad_time']} "
            f"remote_errors={len(errors)} clients={len(payload['trackedClients'])} "
            f"allTimeTokens={totals['totalTokens']} "
            f"allTimeCostUsd={totals['costUsd']:.6f}",
            flush=True,
        )
        if args.verbose and files:
            for item in files:
                print(
                    f"[cobble-export] file {item.get('path')} "
                    f"lines={item.get('lines')} bytes={item.get('bytes')}",
                    flush=True,
                )

    if args.dry_run or args.print_payload:
        return 0

    secret = tm.read_secret(config)
    result = tm.push_with_retry(config, payload, secret)
    print(
        f"[cobble-export] pushed device={result.get('deviceId', config['device_id'])} "
        f"bytes={tm.payload_size(payload)}",
        flush=True,
    )
    return 0


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    try:
        config = load_config(args)
    except ConfigError as exc:
        print(f"[cobble-export] error: {exc}", file=sys.stderr)
        return 2

    lock_handle = None
    try:
        if not (args.dry_run or args.print_payload):
            lock_handle = tm.acquire_lock(Path(config["state_file"]))
        if args.reset_state:
            state_path = Path(config["state_file"])
            try:
                state_path.unlink()
            except FileNotFoundError:
                pass
            print(f"[cobble-export] reset state {state_path}", flush=True)
            return 0

        while True:
            try:
                run_cycle(config, args)
            except tm.PushAuthError as exc:
                print(f"[cobble-export] error: {exc}", file=sys.stderr)
                return 3
            except (ConfigError, tm.PushPayloadError, tm.PushRetryError, OSError) as exc:
                print(f"[cobble-export] error: {exc}", file=sys.stderr)
                if args.once:
                    return 1
            if args.once:
                return 0
            time.sleep(int(config["interval_seconds"]))
    except KeyboardInterrupt:
        return 0
    finally:
        if lock_handle is not None:
            lock_handle.close()


if __name__ == "__main__":
    raise SystemExit(main())
