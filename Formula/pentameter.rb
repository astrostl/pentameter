class Pentameter < Formula
  desc "Prometheus exporter for Pentair IntelliCenter pool controllers"
  homepage "https://github.com/astrostl/pentameter"
  version "0.3.4"
  license "MIT"

  if OS.mac? && Hardware::CPU.arm?
    url "https://github.com/astrostl/pentameter/releases/download/v0.3.4/pentameter-v0.3.4-darwin-arm64.tar.gz"
    sha256 "b2b94321cd2fb46274f3002b0b9f79e3827a2dfc690c976833cc35ca5cfa030a"
  elsif OS.mac? && Hardware::CPU.intel?
    url "https://github.com/astrostl/pentameter/releases/download/v0.3.4/pentameter-v0.3.4-darwin-amd64.tar.gz"
    sha256 "5f45c38a29b03a8cab3e035613bbda87f940e91d1a30bd8708b3a1c54273f7f9"
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