class Pentameter < Formula
  desc "Prometheus exporter for Pentair IntelliCenter pool controllers"
  homepage "https://github.com/astrostl/pentameter"
  version "0.2.0-10-g1a1df08-dirty"
  license "MIT"

  if OS.mac? && Hardware::CPU.arm?
    url "https://github.com/astrostl/pentameter/releases/download/v0.2.0-10-g1a1df08-dirty/pentameter-v0.2.0-10-g1a1df08-dirty-darwin-arm64.tar.gz"
    sha256 "6749598a515fd7f3b0d83577e49cc7e8cafc9ca6ff705fa933ed670082e0a47b"
  elsif OS.mac? && Hardware::CPU.intel?
    url "https://github.com/astrostl/pentameter/releases/download/v0.2.0-10-g1a1df08-dirty/pentameter-v0.2.0-10-g1a1df08-dirty-darwin-amd64.tar.gz"
    sha256 "48a7d7ce6c5f5bb7525c8c69d3031e4aebfb327eefb71a0de7f3a76b63fbb84a"
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