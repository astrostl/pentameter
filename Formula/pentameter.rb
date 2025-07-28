class Pentameter < Formula
  desc "Prometheus exporter for Pentair IntelliCenter pool controllers"
  homepage "https://github.com/astrostl/pentameter"
  version "0.2.1"
  license "MIT"

  if OS.mac? && Hardware::CPU.arm?
    url "https://github.com/astrostl/pentameter/releases/download/v0.2.1/pentameter-v0.2.1-darwin-arm64.tar.gz"
    sha256 "d8d92c820b3ece4104a8d513f0ed1b6c4865bf6c3dcda559077c96a86b5f1c0f"
  elsif OS.mac? && Hardware::CPU.intel?
    url "https://github.com/astrostl/pentameter/releases/download/v0.2.1/pentameter-v0.2.1-darwin-amd64.tar.gz"
    sha256 "ef39a15546961c6e28b9f800c9dec0a4fe992376f1f0e1f90875896dc9179b18"
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