class Pentameter < Formula
  desc "Prometheus exporter for Pentair IntelliCenter pool controllers"
  homepage "https://github.com/astrostl/pentameter"
  version "0.2.1"
  license "MIT"

  if OS.mac? && Hardware::CPU.arm?
    url "https://github.com/astrostl/pentameter/releases/download/v0.2.1/pentameter-v0.2.1-darwin-arm64.tar.gz"
    sha256 "79b10d07eb36d1f0fc4f5bc96d62d8609967be66850683985744aee339aa2af8"
  elsif OS.mac? && Hardware::CPU.intel?
    url "https://github.com/astrostl/pentameter/releases/download/v0.2.1/pentameter-v0.2.1-darwin-amd64.tar.gz"
    sha256 "5ff317e84b427fdf8c702bdb2ad7ea99a75e5ace62dc4f18ea9c67af4f7ac969"
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