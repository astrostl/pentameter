class Pentameter < Formula
  desc "Prometheus exporter for Pentair IntelliCenter pool controllers"
  homepage "https://github.com/astrostl/pentameter"
  version "0.4.2"
  license "MIT"

  if OS.mac? && Hardware::CPU.arm?
    url "https://github.com/astrostl/pentameter/releases/download/v0.4.2/pentameter-v0.4.2-darwin-arm64.tar.gz"
    sha256 "95fb2e0c47c131949fcf7995b281d71b04dac2b3baee4b0b75eea7fab8132579"
  elsif OS.mac? && Hardware::CPU.intel?
    url "https://github.com/astrostl/pentameter/releases/download/v0.4.2/pentameter-v0.4.2-darwin-amd64.tar.gz"
    sha256 "dd4e5d64fad716d16f6987629d5c766d4d055b9eec181725b74c041ad80645e6"
  else
    odie "Pentameter is only supported on macOS via Homebrew. Use Docker or build from source for Linux deployment."
  end

  def install
    bin.install "pentameter-darwin-arm64" => "pentameter" if Hardware::CPU.arm?
    bin.install "pentameter-darwin-amd64" => "pentameter" if Hardware::CPU.intel?
  end

  def caveats
    <<~EOS
      Pentameter requires connection to a Pentair IntelliCenter pool controller.

      Auto-discovery (recommended):
        pentameter

      The IntelliCenter will be automatically discovered via mDNS (pentair.local).

      Manual IP configuration (if auto-discovery fails):
        pentameter --ic-ip 192.168.1.100

      Or via environment variable:
        export PENTAMETER_IC_IP=192.168.1.100
        pentameter

      Metrics are available at: http://localhost:8080/metrics

      For complete monitoring setup with Grafana dashboards:
        https://github.com/astrostl/pentameter
    EOS
  end

  test do
    system bin/"pentameter", "--help"
  end
end