{
  description = "The maintained WakaTime CLI fork";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

    devkit = {
      url = "github:hakula139/devkit/v3";
      inputs.nixpkgs.follows = "nixpkgs";
      inputs.nixpkgs-unstable.follows = "nixpkgs";
    };
  };

  outputs =
    {
      self,
      nixpkgs,
      devkit,
    }:
    devkit.lib.eachSystem devkit.lib.systems (
      system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
        go = pkgs.buildPackages.callPackage ./nix/go.nix { };
        buildGoModule = pkgs.buildGoLatestModule.override { inherit go; };
        wakatime-cli = pkgs.callPackage ./nix/package.nix {
          inherit buildGoModule;
          source = self;
        };
      in
      {
        packages = {
          default = wakatime-cli;
          inherit wakatime-cli;
        };

        checks = {
          package = wakatime-cli;
          version = wakatime-cli.tests.version;
        };
      }
    );
}
