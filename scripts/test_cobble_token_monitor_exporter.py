#!/usr/bin/env python3

from __future__ import annotations

import datetime as dt
import unittest

import cobble_token_monitor_exporter as exporter


def record(
    call_id: str,
    tokens: int,
    *,
    prompt: int = 0,
    completion: int = 0,
    model: str = "deepseek-v4.1-flash",
    timestamp: str = "2026-10-22T13:43:54Z",
) -> dict:
    return {
        "id": call_id,
        "timestamp": timestamp,
        "model": model,
        "prompt_tokens": prompt,
        "completion_tokens": completion,
        "total_tokens": tokens,
        "duration_ms": 100,
        "attempts": 1,
    }


def config() -> dict:
    return {
        "client_id": "cobble",
        "device_id": "cobble-vm",
        "hostname": "xue",
        "ssh_host": "10.28.0.132",
        "utc_offset_minutes": 480,
        "time_zone_name": "Asia/Shanghai",
        "history_daily_max_days": 370,
        "payload_max_bytes": 900_000,
    }


class CobbleExporterTest(unittest.TestCase):
    def test_normalize_model_strips_realm_prefix(self) -> None:
        self.assertEqual(exporter.normalize_model("cn:deepseek-v4.1-flash"), "deepseek-v4.1-flash")
        self.assertEqual(exporter.normalize_model("deepseek-v4.1-flash"), "deepseek-v4.1-flash")
        self.assertEqual(exporter.normalize_model(""), "unknown")

    def test_ingest_deduplicates_and_aggregates_by_day(self) -> None:
        state = exporter.empty_state()
        records = [
            record("a", 120, prompt=100, completion=20, model="cn:deepseek-v4.1-flash"),
            record("a", 120, prompt=100, completion=20, model="cn:deepseek-v4.1-flash"),
            record("b", 80, prompt=50, completion=30, timestamp="2026-10-22T14:00:00Z"),
        ]
        stats = exporter.ingest_records(state, records, config())
        self.assertEqual(stats, {"records": 3, "new": 2, "duplicates": 1, "ignored": 0, "bad_time": 0})
        period = state["days"]["2026-10-22"]
        self.assertEqual(period["totalTokens"], 200)
        self.assertEqual(period["outputTokens"], 50)
        self.assertEqual(period["clients"], {"cobble": 200})
        self.assertEqual(period["models"], {"deepseek-v4.1-flash": 200})

    def test_build_payload_contains_cost_and_device_identity(self) -> None:
        state = exporter.empty_state()
        exporter.ingest_records(
            state,
            [record("a", 120, prompt=100, completion=20)],
            config(),
        )
        rates = {
            "deepseek-v4.1-flash": {
                "inputCostPerToken": 0.000001,
                "outputCostPerToken": 0.000002,
                "cacheReadInputTokenCost": 0.0,
                "cacheCreationInputTokenCost": 0.0,
            }
        }
        payload = exporter.build_payload(
            state,
            config(),
            dt.datetime(2026, 10, 22, 16, tzinfo=dt.timezone.utc),
            rates,
        )
        self.assertEqual(payload["deviceId"], "cobble-vm")
        self.assertEqual(payload["agentRuntime"], "ssh-collector")
        self.assertEqual(payload["trackedClients"], ["cobble"])
        self.assertEqual(payload["periods"]["allTime"]["clients"], {"cobble": 120})
        self.assertEqual(payload["periods"]["allTime"]["models"], {"deepseek-v4.1-flash": 120})
        self.assertAlmostEqual(payload["periods"]["allTime"]["costUsd"], 0.00014, places=12)
        self.assertAlmostEqual(
            payload["periods"]["allTime"]["clientModelCosts"]["cobble"]["deepseek-v4.1-flash"],
            0.00014,
            places=12,
        )


if __name__ == "__main__":
    unittest.main()