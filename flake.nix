{
  description = "dantofa-saas — the dctl CLI, packaged with its runtime CLIs (kind/flux/docker).";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";

    pyproject-nix = {
      url = "github:pyproject-nix/pyproject.nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    uv2nix = {
      url = "github:pyproject-nix/uv2nix";
      inputs.pyproject-nix.follows = "pyproject-nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    pyproject-build-systems = {
      url = "github:pyproject-nix/build-system-pkgs";
      inputs.pyproject-nix.follows = "pyproject-nix";
      inputs.uv2nix.follows = "uv2nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs =
    {
      self,
      nixpkgs,
      uv2nix,
      pyproject-nix,
      pyproject-build-systems,
    }:
    let
      inherit (nixpkgs) lib;

      # hatch-vcs derives the version from git, but a sandboxed Nix build has no
      # .git — so we feed it one. Downstream pins a release tag; the local segment
      # records the flake rev. (A follow-up can wire this to the actual git tag.)
      version = "0.0.0+" + (self.shortRev or self.dirtyShortRev or "dev");

      # uv.lock + pyproject.toml are the single source of truth for the dependency
      # set — the very same lockfile the uv/pip channel installs from.
      workspace = uv2nix.lib.workspace.loadWorkspace { workspaceRoot = ./.; };
      overlay = workspace.mkPyprojectOverlay { sourcePreference = "wheel"; };

      # Give hatch-vcs its version for the root distribution only (not deps).
      pyprojectOverrides = _final: prev: {
        dantofa-saas = prev.dantofa-saas.overrideAttrs (old: {
          env = (old.env or { }) // {
            SETUPTOOLS_SCM_PRETEND_VERSION_FOR_DANTOFA_SAAS = version;
          };
        });
      };

      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      forAllSystems = lib.genAttrs systems;
      pkgsFor = system: nixpkgs.legacyPackages.${system};

      # External CLIs the local-cluster commands shell out to. git is deliberately
      # absent: provenance is read natively via dulwich.
      runtimeTools = pkgs: [
        pkgs.kind
        pkgs.fluxcd
        pkgs.docker-client
      ];

      pythonSetFor =
        system:
        let
          pkgs = pkgsFor system;
        in
        (pkgs.callPackage pyproject-nix.build.packages { python = pkgs.python313; }).overrideScope
          (lib.composeManyExtensions [
            pyproject-build-systems.overlays.default
            overlay
            pyprojectOverrides
          ]);

      dctlFor =
        system:
        let
          pkgs = pkgsFor system;
          pythonSet = pythonSetFor system;
          venv = pythonSet.mkVirtualEnv "dantofa-saas-env" workspace.deps.default;
        in
        # Wrap both entry points so kind/flux/docker travel in the runtime closure
        # — that is what spares Nix consumers a separate tool install.
        pkgs.symlinkJoin {
          name = "dctl-${version}";
          paths = [ venv ];
          nativeBuildInputs = [ pkgs.makeWrapper ];
          postBuild = ''
            for prog in dctl dantofa.cli; do
              wrapProgram "$out/bin/$prog" \
                --prefix PATH : ${lib.makeBinPath (runtimeTools pkgs)}
            done
          '';
        };
    in
    {
      packages = forAllSystems (system: {
        default = dctlFor system;
      });

      apps = forAllSystems (system: {
        default = {
          type = "app";
          program = "${self.packages.${system}.default}/bin/dctl";
        };
      });

      devShells = forAllSystems (
        system:
        let
          pkgs = pkgsFor system;
        in
        {
          default = pkgs.mkShell {
            packages = [
              self.packages.${system}.default
              pkgs.uv
            ];
          };
        }
      );

      # `nix flake check` builds the wrapped package on the current system.
      checks = forAllSystems (system: {
        default = self.packages.${system}.default;
      });
    };
}
