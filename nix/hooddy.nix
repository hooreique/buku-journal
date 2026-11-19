{ self }:
{ lib, config, pkgs, ... }:
let
  cfg = config.services.buku-journal;
  package = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
  address = "127.0.0.1:${toString cfg.port}";
  quoteArg = value: "\"" + lib.replaceStrings [ "\\" "\"" "%" "$" ] [ "\\\\" "\\\"" "%%" "$$" ] value + "\"";
  journalArg = lib.optionalString (cfg.journalDir != null) " --journal-dir ${quoteArg cfg.journalDir}";
in {
  options.services.buku-journal = {
    enable = lib.mkEnableOption "Buku Journal";
    domain = lib.mkOption {
      type = lib.types.strMatching "[a-zA-Z0-9]([a-zA-Z0-9.-]*[.])[a-zA-Z0-9-]*[a-zA-Z0-9]";
      default = "buku-journal.hooreique.com";
    };
    port = lib.mkOption { type = lib.types.ints.between 1024 65535; default = 3002; };
    journalDir = lib.mkOption {
      type = lib.types.nullOr (lib.types.strMatching "/[^\r\n]*");
      default = null;
      description = "Absolute journal directory, or null to use the application's configured directory.";
    };
  };
  config = lib.mkIf cfg.enable {
    hooddy.services.buku-journal = {
      unit = ''
        [Unit]
        Description=Buku Journal
        After=network-online.target
        Wants=network-online.target

        [Service]
        ExecStart=${package}/bin/buku-journal serve --listen ${address}${journalArg}
        Restart=on-failure
        RestartSec=3s
        TimeoutStopSec=30s
        KillMode=mixed
        UMask=0077

        [Install]
        WantedBy=default.target
      '';
      healthCheck = { url = "http://${address}/healthz"; timeoutSeconds = 30; };
    };
    hooddy.sites.buku-journal.config = ''
      ${cfg.domain} {
        header X-Robots-Tag "noindex, nofollow"
        header Referrer-Policy "no-referrer"
        reverse_proxy ${address}
      }
    '';
  };
}
