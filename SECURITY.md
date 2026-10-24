# Security

POSTERM hands out a shell on every machine it's connected to. A security bug
in it is a bug in every one of those machines, so reports get priority over
everything else.

## Reporting

Use GitHub's private vulnerability reporting:
**[Report a vulnerability](https://github.com/l3dlp/posterm.sh/security/advisories/new)**.
Don't open a public issue for it.

Say what you found, how to reproduce it, and which version or commit. You'll
get an answer within 72 hours. The fix ships as a patch release with a public
advisory, credited to you unless you'd rather not.

## Supported versions

| version | security fixes |
| ------- | -------------- |
| latest release | yes |
| older releases | no: upgrade |

Before 1.0, only the latest release is supported. `curl -fsSL
https://posterm.sh/install | sh` always installs it, and checks its signature.

## Threat model

The full reasoning is in
[docs/design.md, Threat model](docs/design.md#threat-model). In short:

- **The operator** holds the token and has a shell everywhere. That's the job.
- **The labs** are agents that read text strangers wrote. They must never be
  able to drive the hub: they don't get its environment, can't read its
  secrets and can't replace them (when the hub starts as root with a
  `POSTERM_USER`).
- **A spoke** joins with a token bound to its own machine name. It can serve
  its labs to the hub and nothing else: no API, no other machine's name.
- **The network** sees TLS only: the reverse proxy terminates it, and spokes
  refuse to send their token in cleartext.
- **Releases** are signed. The installer refuses binaries whose `SHA256SUMS`
  isn't signed by the key in [`site/allowed_signers`](site/allowed_signers).

Out of scope: a compromised operator browser, root on the hub, and agents
doing damage inside the projects they were given. They have a shell there,
on purpose.
