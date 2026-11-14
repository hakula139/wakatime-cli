{
  lib,
  stdenv,
  buildGoModule,
  testers,
  writableTmpDirAsHomeHook,
  source,
}:

let
  timestamp = source.lastModifiedDate;
  date = "${builtins.substring 0 4 timestamp}-${builtins.substring 4 2 timestamp}-${
    builtins.substring 6 2 timestamp
  }";
  buildDate = "${date}T${builtins.substring 8 2 timestamp}:${builtins.substring 10 2 timestamp}:${
    builtins.substring 12 2 timestamp
  }Z";
  versionModule = "github.com/wakatime/wakatime-cli/pkg/version";
in
buildGoModule (finalAttrs: {
  pname = "wakatime-cli";
  version = "2.28.4-unstable-${date}";
  src = source;

  vendorHash = "sha256-Tz0MvNU9iuIMNzWsy/R8+onBb2iZBCAem2XyAH8pVP4=";
  subPackages = [ "." ];
  env.CGO_ENABLED = 0;

  ldflags = [
    "-s"
    "-w"
    "-X ${versionModule}.Version=${finalAttrs.version}"
    "-X ${versionModule}.Commit=${source.rev or source.dirtyRev}"
    "-X ${versionModule}.BuildDate=${buildDate}"
    "-X ${versionModule}.OS=${stdenv.hostPlatform.go.GOOS}"
    "-X ${versionModule}.Arch=${stdenv.hostPlatform.go.GOARCH}"
  ];

  nativeCheckInputs = [ writableTmpDirAsHomeHook ];
  __darwinAllowLocalNetworking = true;

  passthru.tests.version = testers.testVersion {
    package = finalAttrs.finalPackage;
    command = ''HOME="$TMPDIR" wakatime-cli --version'';
  };

  meta = {
    description = "Command line interface to WakaTime";
    homepage = "https://github.com/hakula139/wakatime-cli";
    license = lib.licenses.bsd3;
    mainProgram = "wakatime-cli";
    platforms = lib.platforms.linux ++ lib.platforms.darwin;
  };
})
