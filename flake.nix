# ==================================================================================================
# wakatime-cli Development Flake
# ==================================================================================================

{
  description = "The maintained WakaTime CLI fork";

  nixConfig = {
    extra-substituters = [ "https://hakula.cachix.org" ];
    extra-trusted-public-keys = [
      "hakula.cachix.org-1:7zwB3fhMfReHdOjh6DmnaLXgqbPDBcojvN9F+osZw0k="
    ];
  };

  # ------------------------------------------------------------------------------------------------
  # Inputs
  # ------------------------------------------------------------------------------------------------
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";

    devkit = {
      url = "github:hakula139/devkit/v3";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  # ------------------------------------------------------------------------------------------------
  # Outputs
  # ------------------------------------------------------------------------------------------------
  outputs =
    {
      self,
      nixpkgs,
      devkit,
    }:
    let
      mkWakatimeCli =
        pkgs:
        pkgs.callPackage ./nix/package.nix {
          source = self;
        };

      platformKey = p: {
        inherit (p) config isStatic;
      };
    in
    # ----------------------------------------------------------------------------------------------
    # Overlay
    # ----------------------------------------------------------------------------------------------
    {
      overlays.default =
        _: prev:
        let
          locked = self.packages.${prev.stdenv.hostPlatform.system}.wakatime-cli or null;
          useLocked =
            locked != null
            && prev.stdenv.hostPlatform == prev.stdenv.targetPlatform
            && platformKey prev.stdenv.buildPlatform == platformKey locked.stdenv.buildPlatform
            && platformKey prev.stdenv.hostPlatform == platformKey locked.stdenv.hostPlatform;
        in
        {
          wakatime-cli = if useLocked then locked else mkWakatimeCli prev;
        };
    }
    // devkit.lib.eachSystem devkit.lib.systems (
      system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
        wakatime-cli = mkWakatimeCli pkgs;
      in
      {
        # ------------------------------------------------------------------------------------------
        # Packages
        # ------------------------------------------------------------------------------------------
        packages = {
          default = wakatime-cli;
          inherit wakatime-cli;
        };

        # ------------------------------------------------------------------------------------------
        # Checks
        # ------------------------------------------------------------------------------------------
        checks = {
          package = wakatime-cli;
          version = wakatime-cli.tests.version;

          overlay =
            let
              inherit (pkgs) pkgsCross pkgsMusl;
              inherit (pkgs.stdenv.hostPlatform) isLinux;

              canaryPkgs = pkgs.extend (
                _: _: {
                  buildGo127Module = throw "consumer package reconstruction";
                }
              );
              extended = canaryPkgs.extend self.overlays.default;
              fallbackPkgs = if isLinux then pkgsMusl else pkgsCross.aarch64-multiplatform;
              fallback = (fallbackPkgs.extend self.overlays.default).wakatime-cli;
            in
            assert extended.wakatime-cli.drvPath == wakatime-cli.drvPath;
            assert fallback.pname == "wakatime-cli";
            assert fallback.drvPath != wakatime-cli.drvPath;
            pkgs.emptyFile;
        };

        # ------------------------------------------------------------------------------------------
        # Formatter
        # ------------------------------------------------------------------------------------------
        formatter = pkgs.nixfmt-tree;
      }
    );
}
