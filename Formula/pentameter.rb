class Pentameter < Formula
  desc "Prometheus exporter for Pentair IntelliCenter pool controllers"
  homepage "https://github.com/astrostl/pentameter"
  version "0.6.1"
  license "MIT"

  if OS.mac? && Hardware::CPU.arm?
    url "https://github.com/astrostl/pentameter/releases/download/v0.6.1/pentameter-v0.6.1-darwin-arm64.tar.gz"
    sha256 "d01e061f09927c3a21656fcbca83c1a4271114afdb4529f8ee8debee4a306fd3"
  elsif OS.mac? && Hardware::CPU.intel?
    url "https://github.com/astrostl/pentameter/releases/download/v0.6.1/pentameter-v0.6.1-darwin-amd64.tar.gz"
    sha256 "f2afd324f451ef0f2431da708190762afc181a951df93627d265936c1243f1d4"
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