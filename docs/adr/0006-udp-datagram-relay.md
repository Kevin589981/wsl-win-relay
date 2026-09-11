# ADR-0006: Relay UDP as Endpoint-Preserving Datagrams

## Status
Accepted

## Context

TCP CONNECT does not cover DNS, QUIC, or other UDP applications. Treating UDP as a byte stream would lose message boundaries and source/destination metadata.

## Decision

Give each UDP association a Windows-side UDP socket. Carry every packet in one bounded frame containing a textual endpoint and the unchanged datagram payload. SOCKS5 UDP ASSOCIATE owns one such relay socket and ends when its TCP control connection closes.

Domain destinations remain textual through the relay protocol. Native Windows
UDP resolves them before sending; a SOCKS5H upstream can instead receive the
domain form and resolve it remotely. Response frames contain the source
endpoint observed by the selected packet path. The SOCKS adapter accepts
packets only from the TCP client's IP and remembers the client's UDP endpoint
from valid packets.

Reject SOCKS packets with `FRAG != 0`; common SOCKS clients do not implement the optional fragmentation mechanism and silently inventing reassembly rules would be unsafe.

## Consequences

### Positive

- UDP never depends on WSL DNS or HNS networking.
- Message boundaries and response source addresses are preserved.
- One multiplexed transport can carry concurrent TCP and UDP flows.

### Negative

- Datagram loss can occur under transport failure; UDP has no retransmission contract.
- SOCKS5 clients must support UDP ASSOCIATE.
- Transparent UDP still requires a future TUN or interception adapter.
