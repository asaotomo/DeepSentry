# Security Policy

## Supported versions

Security fixes are provided for the latest published DeepSentry release.

| Version | Supported |
| --- | --- |
| 2.0.4 | Yes |
| 2.0.3 | Yes |
| Earlier releases | No |

## Reporting a vulnerability

Please use the repository's **GitHub Security Advisories → Report a vulnerability** flow. Do not open a public issue for a suspected vulnerability, exposed credential, authentication bypass, unsafe command execution, or sensitive-data disclosure.

Include the affected version and platform, the smallest reproducible case, expected and actual behavior, security impact, and any suggested mitigation. Remove real API keys, passwords, tokens, private hostnames, customer data, and production evidence from the report.

The maintainers will acknowledge a complete report, validate its impact, prepare a fix, and coordinate disclosure through a security advisory. Please do not test against systems you do not own or have explicit authorization to assess.

## Operational safety

DeepSentry is an administrative and security-response tool. Run it only on authorized targets, keep production changes behind interactive approval, and prefer encrypted protocols such as SSH/SFTP/FTPS. Local `config.yaml`, reports, traces, checkpoints, artifacts, and workspace files may contain sensitive operational data and are intentionally excluded by `.gitignore`.
