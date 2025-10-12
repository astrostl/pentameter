class Pentameter < Formula
  desc "Prometheus exporter for Pentair IntelliCenter pool controllers"
  homepage "https://github.com/astrostl/pentameter"
  version "0.3.0"
  license "MIT"

  if OS.mac? && Hardware::CPU.arm?
    url "https://github.com/astrostl/pentameter/releases/download/v0.3.0/pentameter-v0.3.0-darwin-arm64.tar.gz"
    sha256 "df50ffa6c7f73c16992ea9f2f32c7816cad61f17e57044ccf15648003e07e1f3"
  elsif OS.mac? && Hardware::CPU.intel?
    url "https://github.com/astrostl/pentameter/releases/download/v0.3.0/pentameter-v0.3.0-darwin-amd64.tar.gz"
    sha256 "14f1a2b6defce4ff0c7a22d58d36f76b0a67341eb3cefff64619fce2d9709699"
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

      Set your IntelliCenter IP address:
        export PENTAMETER_IC_IP=192.168.1.100

      Start the exporter:
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