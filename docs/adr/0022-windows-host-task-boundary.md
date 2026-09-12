# ADR-0022: Offer a Windows Host Task Boundary for the Broker

## Status

Accepted

## Context

The WSL user service is convenient for installation, but its lifetime is tied
to the WSL user session and VM. A WSL shutdown can therefore stop the broker
before a later WSL proxy invocation can reconnect. The broker's own supervisor
only recovers its frontend; it cannot outlive the host process that launched
it.

## Decision

Provide an optional `scripts/install-broker-windows-task.ps1` installer for the
Windows Task Scheduler. It registers the broker's `-supervise` parent at user
logon, passes only the endpoint and a protected `-token-file` path, and applies
bounded task-level restart settings. The task is a host deployment boundary;
the WSL proxy continues to attach through the existing per-user named pipe and
WSLENV credential contract.

The installer rejects missing, non-regular, or reparse-point token files and
removes inherited ACL entries before granting the selected user read access.
It is optional and does not replace the WSL systemd user unit.

## Consequences

- The Windows broker can remain available while WSL is stopped and can accept
  a later connector attach without requiring HNS or mirrored networking.
- The task runs with the user's interactive identity and does not create a
  machine-wide network listener.
- A socket-owner crash still terminates established kernel sockets; the task
  boundary only keeps the broker recovery tree available for future sessions.
