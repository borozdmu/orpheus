#!/usr/bin/env python3
"""Read-only Docker fleet gate before enabling trusted input masking.

No environment variables, configuration contents, keys or container logs are read.
The operator must freeze deployment and supply every context able to reach the DB.
"""

import argparse
import json
import re
import subprocess
import sys

SCOPE = "io.orpheus.database-scope"
ROLE = "io.orpheus.role"
REVISION = "org.opencontainers.image.revision"
# Deliberately project only non-secret Docker metadata, never Config.Env.
CONTAINER_FORMAT = (
    '{"id":{{json .Id}},"image":{{json .Image}},'
    '"labels":{{json .Config.Labels}},"path":{{json .Path}},'
    '"args":{{json .Args}},"name":{{json .Name}},'
    '"image_name":{{json .Config.Image}},"running":{{json .State.Running}}}'
)


def docker(context, *args):
    result = subprocess.run(
        ["docker", "--context", context, *args],
        capture_output=True,
        text=True,
        timeout=30,
        check=False,
    )
    if result.returncode:
        # Docker's raw diagnostics can contain operator configuration. Fail closed.
        raise ValueError("Docker inventory is unavailable")
    return result.stdout


def validate(containers, scope, image):
    roles = {"api": 0, "worker": 0}
    for container in containers:
        labels = container.get("labels") or {}
        command = " ".join([container["path"], *container["args"]])
        looks_like_orpheus = (
            "orpheus" in command
            or "orpheus" in container["image_name"].lower()
            and "tools" not in container["image_name"].lower()
        )
        if labels.get(SCOPE) != scope:
            if looks_like_orpheus and not labels.get(SCOPE):
                raise ValueError(
                    "Unscoped Orpheus container exists; inventory is incomplete"
                )
            continue
        role = labels.get(ROLE)
        if role not in roles or container["image"] != image:
            raise ValueError(
                "Target database scope has an unknown role or an old image"
            )
        # Labels alone do not prove what process is configured to execute.
        expected = "serve" if role == "api" else "worker"
        if not looks_like_orpheus or expected not in container["args"]:
            raise ValueError("Container role does not match its configured command")
        if container["running"]:
            roles[role] += 1
    if not all(roles.values()):
        raise ValueError("Both current API and worker must be running")
    return roles


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--context", action="append", required=True)
    parser.add_argument(
        "--scope", required=True, help="Non-secret database fleet identifier"
    )
    parser.add_argument(
        "--revision", required=True, help="Exact approved 40-character commit"
    )
    parser.add_argument(
        "--image", required=True, help="Exact tested local sha256 image ID"
    )
    parser.add_argument(
        "--exclusive-docker-fleet",
        action="store_true",
        required=True,
        help="Attest all DB-capable processes are in these frozen Docker contexts",
    )
    args = parser.parse_args(argv)
    if not re.fullmatch(r"[0-9a-f]{40}", args.revision) or not re.fullmatch(
        r"sha256:[0-9a-f]{64}", args.image
    ):
        parser.error("Exact revision and image ID are required")
    try:
        containers = []
        for context in set(args.context):
            revision = docker(
                context,
                "image",
                "inspect",
                "--format",
                '{{index .Config.Labels "' + REVISION + '"}}',
                args.image,
            ).strip()
            if revision != args.revision:
                raise ValueError(
                    "Image revision is missing or differs from the approved commit"
                )
            # Include stopped containers: they must not be restartable old replicas.
            ids = docker(context, "ps", "--all", "--quiet").split()
            for container_id in ids:
                raw = docker(
                    context, "inspect", "--format", CONTAINER_FORMAT, container_id
                )
                containers.append(json.loads(raw))
        roles = validate(containers, args.scope, args.image)
    except (ValueError, KeyError, TypeError, subprocess.SubprocessError, OSError):
        print(
            "BLOCKED: fleet, image revision, roles or inventory could not be verified.",
            file=sys.stderr,
        )
        return 1
    print(
        f"FLEET_PREFLIGHT_OK api={roles['api']} worker={roles['worker']}; deployment freeze must remain in force"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
