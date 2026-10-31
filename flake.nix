{
  description = "부끄일기 — 파일로 남기는 우리의 기록";
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  outputs = { self, nixpkgs }:
    let
      systems = [ "aarch64-linux" "x86_64-linux" ];
      eachSystem = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in {
      packages = eachSystem (pkgs: rec {
        default = pkgs.callPackage ./nix/package.nix { };
        buku-journal = default;
        deploy = pkgs.writeShellApplication {
          name = "buku-journal-deploy";
          runtimeInputs = [ pkgs.coreutils pkgs.curl pkgs.gnugrep pkgs.systemd pkgs.nix pkgs.util-linux ];
          text = builtins.replaceStrings [ "@APP@" ] [ "${default}" ] (builtins.readFile ./scripts/deploy.sh);
        };
      });
      apps = eachSystem (pkgs: {
        default = { type = "app"; program = "${self.packages.${pkgs.stdenv.hostPlatform.system}.default}/bin/buku-journal"; };
        deploy = { type = "app"; program = "${self.packages.${pkgs.stdenv.hostPlatform.system}.deploy}/bin/buku-journal-deploy"; };
      });
      checks = eachSystem (pkgs: {
        app = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
        deploy = self.packages.${pkgs.stdenv.hostPlatform.system}.deploy;
        deployment = pkgs.runCommand "buku-journal-deployment-tests" {
          nativeBuildInputs = [ pkgs.python3 pkgs.bash pkgs.coreutils pkgs.gnugrep pkgs.util-linux pkgs.systemd ];
          SYSTEMD_UNIT_PATH = "${pkgs.systemd}/example/systemd/user";
        } ''
          python3 ${./scripts}/test-deploy.py
          touch "$out"
        '';
      });
      devShells = eachSystem (pkgs: {
        default = pkgs.mkShell { packages = [ pkgs.go pkgs.jujutsu pkgs.codex pkgs.ripgrep pkgs.curl pkgs.nodejs ]; };
      });
    };
}
