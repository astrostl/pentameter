class Pentameter < Formula
  desc "Prometheus exporter for Pentair IntelliCenter pool controllers"
  homepage "https://github.com/astrostl/pentameter"
  version "0.4.7"
  license "MIT"

  if OS.mac? && Hardware::CPU.arm?
    url "https://github.com/astrostl/pentameter/releases/download/v0.4.7/pentameter-v0.4.7-darwin-arm64.tar.gz"
    sha256 "1caad097566c7f2b191e80aa28b9f83d8f08cc69680246a0b161d0c6aed95ca2"
  elsif OS.mac? && Hardware::CPU.intel?
    url "https://github.com/astrostl/pentameter/releases/download/v0.4.7/pentameter-v0.4.7-darwin-amd64.tar.gz"
    sha256 "3b6cbcd971180ab99b2ccfe96bfd058e6872c87ae5d5a9468924557f9f50cb5f"
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