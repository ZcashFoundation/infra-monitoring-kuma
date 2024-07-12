---
# status and date are the only required elements. Feel free to remove the rest.
status: approved
date: 2024-07-12
story: [ZF Engineering update: 2024 Sprint 13 (June 18th - July 1st.)](https://forum.zcashcommunity.com/t/zf-engineering-update-2024-sprint-13-june-18th-july-1st/48210#:~:text=DNSSeeder%20not%20responding%20to%20requests)
---

# Implementing Uptime Kuma for monitoring DNS Seeder and other services

## Context & Problem Statement

The Foundation has multiple production services supporting the network, and we need to monitor them, as we've recently had issues with our DNS Seeder service, and the team realizing after CI failed in Zebra. We need to have a monitoring tool to keep track of the services' health and performance, and be notified proactively when something goes wrong.

## Priorities & Constraints <!-- optional -->

* We should have monitoring for all production services.
* We should have a way to be notified when something goes wrong.
* We should have a way to verify the health and performance of the services.
* We should avoid subscription costs, if possible.
* The tool should be able to monitor DNS servers.

## Considered Options

* Option 1: [Datadog](https://www.datadoghq.com/blog/monitor-dns-with-datadog/)
* Option 2: [Atatus](https://www.atatus.com/synthetic-monitoring/features)
* Option 3: [Uptime Kuma](https://github.com/louislam/uptime-kuma)

## Decision Outcome

Chosen option: [Uptime Kuma](https://github.com/louislam/uptime-kuma)

We chose Uptime Kuma because it's open-source, and it's a self-hosted solution, which means we can avoid subscription costs. It also has a simple and clean UI, and it's easy to set up. It supports monitoring DNS servers, and it has a notification system that can be configured to send alerts when something goes wrong.

### Expected Consequences <!-- optional -->

* We will need to set up infrastructure to host Uptime Kuma.
* There's an initial learning curve to set up and configure Uptime Kuma.
* We will have an overhead of maintaining Uptime Kuma.
* We can use Uptime Kuma to monitor other services in the future.
* We'll be able publish a public monitoring dashboard for the community.

## More Information <!-- optional -->

* PR implementing Uptime Kuma: [[ZcashFoundation/infra-monitoring#1]](https://github.com/ZcashFoundation/infra-monitoring-kuma/pull/1)(
