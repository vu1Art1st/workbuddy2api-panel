#!/usr/bin/env python3

from __future__ import annotations

import datetime as dt
import unittest

import artex_mobile_token_monitor_exporter as exporter


def row(
    row_id: int,
    *,
    input_tokens: int = 1000,
    output_tokens: int = 100,
    cache_read: int = 400,
    cache_write: int = 0,
    model: str = "deepseek-v4.1-flash",
    ts: str = "2026-10-23T02:02:01.809Z",
) -> dict:
    return {
        "id": row_id,
        "ts": ts,
        "task_id": "22",
        "exploration_id": 22,
        "worker": "worker",
        "model": model,
        "profile_name": "\u79fb\u52a8\u4e91-ds",
        "latency_ms": 1234,
        "input_tokens": input_tokens,
        "output_tokens": output_tokens,
        "cache_read": cache_read,
        "cache_write": cache_write,
        "status": "ok",
    }


def config() -> dict:
    return {
        "client_id": "artex-mobile",
        "device_id": "artex-mobile-cloud",
        "hostname": "kali",
        "ssh_host": "10.28.0.131",
        "utc_offset_minutes": 480,
        "time_zone_name": "Asia/Shanghai",
        "history_daily_max_days": 370,
        "payload_max_bytes": 900_000,
    }


class ArtexMobileExporterTest(unittest.TestCase):
    def test_ingest_maps_tokens_and_advances_last_id(self) -> None:
        state = exporter.empty_state()
        stats = exporter.ingest_rows(state, [row(10), row(11, input_tokens=0, output_tokens=0, cache_read=0)], config())
        self.assertEqual(stats["new"], 1)
        self.assertEqual(stats["ignored"], 1)
        self.assertEqual(state["last_id"], 11)
        period = state["days"]["2026-10-23"]
        self.assertEqual(period["totalTokens"], 1100)
        self.assertEqual(period["cacheReadTokens"], 400)
        self.assertEqual(period["outputTokens"], 100)
        self.assertEqual(period["unclassifiedTokens"], 0)
        self.assertEqual(period["clients"], {"artex-mobile": 1100})
        self.assertEqual(period["models"], {"deepseek-v4.1-flash": 1100})

    def test_duplicate_ids_are_ignored(self) -> None:
        state = exporter.empty_state()
        exporter.ingest_rows(state, [row(20)], config())
        stats = exporter.ingest_rows(state, [row(20), row(21)], config())
        self.assertEqual(stats["new"], 1)
        self.assertEqual(stats["ignored"], 1)
        self.assertEqual(state["last_id"], 21)

    def test_build_payload_contains_mobile_device_cost_and_cache(self) -> None:
        state = exporter.empty_state()
        exporter.ingest_rows(state, [row(30)], config())
        rates = {
            "deepseek-v4.1-flash": {
                "inputCostPerToken": 0.000001,
                "outputCostPerToken": 0.000002,
                "cacheReadInputTokenCost": 0.0000001,
                "cacheCreationInputTokenCost": 0.0,
            }
        }
        payload = exporter.build_payload(
            state,
            config(),
            dt.datetime(2026, 10, 23, 6, tzinfo=dt.timezone.utc),
            rates,
        )
        self.assertEqual(payload["deviceId"], "artex-mobile-cloud")
        self.assertEqual(payload["agentRuntime"], "artex-ssh-collector")
        self.assertEqual(payload["trackedClients"], ["artex-mobile"])
        all_time = payload["periods"]["allTime"]
        self.assertEqual(all_time["totalTokens"], 1100)
        self.assertEqual(all_time["cacheReadTokens"], 400)
        self.assertAlmostEqual(all_time["costUsd"], 0.00084, places=12)


if __name__ == "__main__":
    unittest.main()
