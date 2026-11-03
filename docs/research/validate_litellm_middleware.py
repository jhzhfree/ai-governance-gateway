"""Run against an unchanged LiteLLM checkout; requires Python 3.10+ and starlette.

Usage: python3 docs/research/validate_litellm_middleware.py /path/to/litellm
This tests only the reservation ownership middleware, not budget accounting.
"""

import asyncio
import importlib.util
from pathlib import Path
import sys
import unittest


source = Path(sys.argv.pop(1)) / "litellm/proxy/middleware/budget_reservation_release_middleware.py"
spec = importlib.util.spec_from_file_location("upstream_middleware", source)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class ReservationMiddlewareTests(unittest.IsolatedAsyncioTestCase):
    async def exercise(self, scope, failure=None, release_failure=None):
        calls = []

        async def app(scope, receive, send):
            calls.append("app")
            if failure:
                raise failure

        async def release(reservation):
            calls.append(reservation)
            if release_failure:
                raise release_failure

        middleware = module.BudgetReservationReleaseMiddleware(app, release)
        try:
            await middleware(scope, None, None)
        finally:
            self.calls = calls

    async def test_http_releases_reservation(self):
        reservation = {"finalized": False}
        await self.exercise({"type": "http", "state": {"budget_reservation": reservation}})
        self.assertEqual(self.calls, ["app", reservation])

    async def test_websocket_releases_reservation(self):
        reservation = {"finalized": False}
        await self.exercise({"type": "websocket", "state": {"budget_reservation": reservation}})
        self.assertEqual(self.calls, ["app", reservation])

    async def test_exception_still_releases(self):
        reservation = {"finalized": False}
        with self.assertRaisesRegex(ValueError, "upstream"):
            await self.exercise(
                {"type": "http", "state": {"budget_reservation": reservation}}, ValueError("upstream")
            )
        self.assertEqual(self.calls, ["app", reservation])

    async def test_cancel_still_calls_release(self):
        reservation = {"finalized": False}
        with self.assertRaises(asyncio.CancelledError):
            await self.exercise(
                {"type": "http", "state": {"budget_reservation": reservation}}, asyncio.CancelledError()
            )
        self.assertEqual(self.calls, ["app", reservation])

    async def test_no_reservation_or_other_scope(self):
        for scope in ({"type": "http"}, {"type": "http", "state": None}, {"type": "lifespan"}):
            with self.subTest(scope=scope):
                await self.exercise(scope)
                self.assertEqual(self.calls, ["app"])

    async def test_release_failure_propagates(self):
        with self.assertRaisesRegex(RuntimeError, "release failed"):
            await self.exercise(
                {"type": "http", "state": {"budget_reservation": {}}},
                release_failure=RuntimeError("release failed"),
            )


if __name__ == "__main__":
    unittest.main(verbosity=2)
