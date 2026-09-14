# Monitoring

Netdata dashboard: https://monitor.sir-labs.com (sir-labs login required).
The service publishes no host port. The existing gateway forwards traffic over
`sir-server_sir-net`. Named volumes retain configuration and metric history.

```sh
cd service/monitor
docker compose -f compose.yaml -f compose.sir.yaml config --quiet
docker compose -f compose.yaml -f compose.sir.yaml up -d --wait
```

The Deploy monitor workflow deploys changes on main using the existing
`self-hosted, sir-labs` runner. It can also be started manually.

Host CPU, RAM, disks, processes and Docker containers are collected using the
Netdata-recommended read-only host mounts and monitoring capabilities.
Bridge networking allows gateway access without a public Netdata port; host
network-stack discovery is limited compared with Netdata host networking.
Anonymous telemetry is disabled. No Netdata Cloud account is configured.

Installation reference: https://learn.netdata.cloud/docs/netdata-agent/installation/docker
