"""Post real workflow state; credentials are supplied only to this step."""

import json, os, time, urllib.request, urllib.error


def main():
    token = os.getenv("DOMAINS_DEPLOY_TOKEN", "")
    if not token:
        print("Deployment event skipped: repository token not configured")
        return
    body = json.dumps(
        {
            "host": os.environ["DOMAINS_DEPLOY_HOST"],
            "status": os.environ["DOMAINS_DEPLOY_STATUS"],
            "commit_sha": os.environ["GITHUB_SHA"],
            "run_url": os.environ["GITHUB_SERVER_URL"]
            + "/"
            + os.environ["GITHUB_REPOSITORY"]
            + "/actions/runs/"
            + os.environ["GITHUB_RUN_ID"],
        }
    ).encode()
    for attempt in range(3):
        try:
            req = urllib.request.Request(
                "https://domains.sir-labs.com/api/v1/deployments",
                data=body,
                headers={
                    "Content-Type": "application/json",
                    "Authorization": "Bearer " + token,
                    "User-Agent": "sir-deployment-reporter/1.0",
                },
            )
            with urllib.request.urlopen(req, timeout=10) as response:
                print("Deployment event recorded:", response.status)
            return
        except (OSError, urllib.error.HTTPError):
            if attempt == 2:
                raise SystemExit(
                    "Deployment event could not be recorded; inspect Domains availability and repository claim"
                )
            time.sleep(1)


if __name__ == "__main__":
    main()
