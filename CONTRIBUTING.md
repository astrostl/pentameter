# Contributing to Pentameter

Thank you for your interest in contributing to Pentameter! We welcome all contributions, from bug reports to new features.

## Getting Started

### Issues

Issues are welcome! Please use them to:
- Report bugs or unexpected behavior
- Request new features or enhancements
- Ask questions about usage or configuration
- Discuss potential improvements

When reporting a bug, please include:
- Your environment (OS, Go version, Docker version if applicable)
- Steps to reproduce the issue
- Expected vs actual behavior
- Relevant logs or error messages

### Pull Requests

Pull requests are welcome! For larger changes, consider opening an issue first to discuss the approach.

#### Before submitting:
1. Run the quality checks: `make quality`
2. Test your changes locally
3. Update documentation if needed

#### PR Guidelines:
- Keep changes focused and atomic
- Include clear commit messages
- Add tests for new functionality when possible
- Update the README if adding new features or configuration options

## Development Setup

```bash
# Clone the repository
git clone https://github.com/astrostl/pentameter.git
cd pentameter

# Install dependencies
go mod tidy

# Build and test
make build
make quality

# Run locally (requires IntelliCenter IP)
./pentameter --ic-ip YOUR_INTELLICENTER_IP
```

## Code Style

This project uses:
- Standard Go formatting (`gofmt`)
- golangci-lint for code quality
- Conventional Go project structure

Run `make quality` to check formatting and linting before submitting.

## Questions?

Feel free to open an issue for any questions about contributing or using Pentameter.