{
  description = "부끄일기 — 파일로 남기는 우리의 기록";
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  outputs = { self, nixpkgs }:
    let
      systems = [ "aarch64-linux" "x86_64-linux" ];
      eachSystem = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in {
      hooddyModules.default = import ./nix/hooddy.nix { inherit self; };
      packages = eachSystem (pkgs: rec {
        default = pkgs.callPackage ./nix/package.nix { };
        buku-journal = default;
      });
      apps = eachSystem (pkgs: {
        default = { type = "app"; program = "${self.packages.${pkgs.stdenv.hostPlatform.system}.default}/bin/buku-journal"; };
      });
      checks = eachSystem (pkgs: {
        app = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
      });
      devShells = eachSystem (pkgs: {
        default = pkgs.mkShell { packages = [ pkgs.go pkgs.jujutsu pkgs.codex pkgs.ripgrep pkgs.curl pkgs.nodejs ]; };
      });
    };
}
