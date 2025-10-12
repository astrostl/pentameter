class Pentameter < Formula
  desc "Prometheus exporter for Pentair IntelliCenter pool controllers"
  homepage "https://github.com/astrostl/pentameter"
  version "0.3.2"
  license "MIT"

  if OS.mac? && Hardware::CPU.arm?
    url "https://github.com/astrostl/pentameter/releases/download/v0.3.2/pentameter-v0.3.2-darwin-arm64.tar.gz"
    sha256 "cde6d7a1cf594d3cc4bc059815b7cae9e38093b8be77adc50c54fd35674ea9af"
  elsif OS.mac? && Hardware::CPU.intel?
    url "https://github.com/astrostl/pentameter/releases/download/v0.3.2/pentameter-v0.3.2-darwin-amd64.tar.gz"
    sha256 "6d6e179e1ee1fdff22e8b6d6d193169f115f92683d6775a86bc6f3480c529dec"
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