{ lib, buildGoModule, makeWrapper, jujutsu, codex, bash, coreutils, ripgrep, findutils, gnugrep, gnused }:
buildGoModule {
  pname = "buku-journal";
  version = "0.1.0";
  src = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [ ../go.mod ../cmd ../internal ../web ];
  };
  vendorHash = null;
  env.CGO_ENABLED = "0";
  subPackages = [ "cmd/buku-journal" ];
  nativeBuildInputs = [ makeWrapper ];
  nativeCheckInputs = [ jujutsu bash coreutils ];
  checkPhase = ''
    runHook preCheck
    go test ./...
    go vet ./...
    runHook postCheck
  '';
  ldflags = [ "-s" "-w" ];
  postFixup = ''
    wrapProgram "$out/bin/buku-journal" \
      --prefix PATH : ${lib.makeBinPath [ codex jujutsu bash coreutils ripgrep findutils gnugrep gnused ]}
  '';
  meta = {
    description = "Private, file-based relationship journal with a Codex worker";
    mainProgram = "buku-journal";
    platforms = [ "aarch64-linux" "x86_64-linux" ];
  };
}
