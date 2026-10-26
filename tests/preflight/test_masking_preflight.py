import importlib.util
import unittest
from pathlib import Path
from unittest.mock import patch

MODULE = Path(__file__).resolve().parents[2] / "tools" / "masking-preflight.py"
spec = importlib.util.spec_from_file_location("preflight", MODULE)
preflight = importlib.util.module_from_spec(spec)
spec.loader.exec_module(preflight)
IMAGE = "sha256:" + "a" * 64
REVISION = "b" * 40


def container(role, *, image=IMAGE, scope="shared", running=True):
    return {
        "labels": {preflight.SCOPE: scope, preflight.ROLE: role},
        "image": image,
        "path": "/orpheus",
        "args": ["serve" if role == "api" else "worker"],
        "image_name": "orpheus:approved",
        "running": running,
    }


class PreflightTests(unittest.TestCase):
    def test_current_api_and_worker(self):
        self.assertEqual(
            preflight.validate(
                [container("api"), container("worker")], "shared", IMAGE
            ),
            {"api": 1, "worker": 1},
        )

    def test_old_api_or_worker_or_stopped_replica_is_blocked(self):
        for role in ["api", "worker"]:
            for running in [True, False]:
                with self.subTest(role=role, running=running):
                    fleet = [container("api"), container("worker")]
                    fleet.append(container(role, image="sha256:old", running=running))
                    with self.assertRaises(ValueError):
                        preflight.validate(fleet, "shared", IMAGE)

    def test_unscoped_unknown_or_missing_role_is_blocked(self):
        for bad in [container("api", scope=""), container("other")]:
            with self.assertRaises(ValueError):
                preflight.validate(
                    [container("api"), container("worker"), bad], "shared", IMAGE
                )
        with self.assertRaises(ValueError):
            preflight.validate([container("api")], "shared", IMAGE)
        bad = container("worker")
        bad["args"] = ["serve"]
        with self.assertRaises(ValueError):
            preflight.validate([container("api"), bad], "shared", IMAGE)

    def test_unknown_image_revision_is_blocked(self):
        args = [
            "--context",
            "fixture",
            "--scope",
            "shared",
            "--revision",
            REVISION,
            "--image",
            IMAGE,
            "--exclusive-docker-fleet",
        ]
        with patch.object(preflight, "docker", return_value="old-revision"):
            self.assertEqual(preflight.main(args), 1)

    def test_inventory_never_requests_environment_or_logs(self):
        self.assertNotIn("Env", preflight.CONTAINER_FORMAT)
        self.assertNotIn("logs", preflight.CONTAINER_FORMAT)


if __name__ == "__main__":
    unittest.main()
