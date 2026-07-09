{
  description = "dantofa-saas — the dctl CLI, packaged with its runtime CLIs (kind/flux/docker/git).";

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
      # .git — so we feed it a version derived purely from the flake source: the
      # commit date as the PEP 440 dev segment, plus the short rev as the local
      # segment. Downstream tracks this flake by rev (e.g. a devbox input on
      # `master` that `devbox update` locks to the latest SHA), so `dctl --version`
      # names the exact commit they run — and changes when they update. There are
      # no release tags in this model; a flake cannot read git tags anyway.
      version =
        "0.0.0.dev"
        + (builtins.substring 0 8 (self.lastModifiedDate or "00000000"))
        + "+g"
        + (self.shortRev or self.dirtyShortRev or "dev");

      # uv.lock + pyproject.toml are the single source of truth for the dependency
      # set — the very same lockfile the uv/pip channel installs from.
      workspace = uv2nix.lib.workspace.loadWorkspace { workspaceRoot = ./.; };
      overlay = workspace.mkPyprojectOverlay { sourcePreference = "wheel"; };

      # Give hatch-vcs its version for the root distribution only (not deps). The
      # override is scoped to this one derivation, so the *unsuffixed*
      # SETUPTOOLS_SCM_PRETEND_VERSION is safe (nothing else builds here). The
      # dist-suffixed SETUPTOOLS_SCM_PRETEND_VERSION_FOR_DANTOFA_SAAS is silently
      # ignored under this build and leaves the version at the 0.0.0 fallback — the
      # `nix` workflow asserts against that regression.
      pyprojectOverrides = _final: prev: {
        dantofa-saas = prev.dantofa-saas.overrideAttrs (old: {
          env = (old.env or { }) // {
            SETUPTOOLS_SCM_PRETEND_VERSION = version;
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

      # Dev-shell packages come from the same pinned nixpkgs as the package (one
      # resolver, one lockfile — no devbox/nixhub drift). bws is unfree, so allow
      # exactly that one package rather than opening allowUnfree globally.
      devPkgsFor =
        system:
        import nixpkgs {
          inherit system;
          config.allowUnfreePredicate = pkg: builtins.elem (lib.getName pkg) [ "bws" ];
        };

      # External CLIs the CLI shells out to, bundled into the package closure so
      # Nix consumers need no separate install. git is included for the OCI-stamp
      # provenance read (HEAD / remote / branch / dirty). The pip/uvx channel does
      # not get these — it relies on them being on the host PATH.
      runtimeTools = pkgs: [
        pkgs.kind
        pkgs.fluxcd
        pkgs.docker-client
        pkgs.git
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
        # Wrap both entry points so kind/flux/docker/git travel in the runtime
        # closure — that is what spares Nix consumers a separate tool install.
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
          meta.description = "Run the dctl CLI (bundled with kind/flux/docker/git).";
        };
      });

      devShells = forAllSystems (
        system:
        let
          pkgs = devPkgsFor system;
        in
        {
          # The development environment (replaces devbox.json). Generic dev/CI
          # tooling from the flake's pinned nixpkgs, plus the runtime CLIs shared
          # with the package via `runtimeTools` — so an editable `uv run dctl`
          # shells out to the same kind/flux/docker/git the packaged dctl bundles.
          # Enter with `nix develop` (or `direnv`); dctl itself runs editable via
          # `uv run -- dantofa.cli` / `just run`, not a prebuilt wrapper.
          default = pkgs.mkShell {
            packages = (runtimeTools pkgs) ++ [
              pkgs.uv
              pkgs.just
              pkgs.actionlint
              pkgs.yamllint
              pkgs.gh
              pkgs.ratchet
              pkgs.shellcheck
              pkgs.bws
              pkgs.kubectl
            ];
            # Local kubeconfig target, matching the old devbox env.
            KUBECONFIG = ".kubeconfig";
            shellHook = ''
              # Load local, gitignored config/secrets (e.g. BWS_ACCESS_TOKEN,
              # BWS_PROJECT_ID) — see .env.example.
              if [ -f .env ]; then
                set -a
                . ./.env
                set +a
              fi
              # Install git hooks for interactive dev only (skip under CI).
              if [ -z "''${CI:-}" ] && [ -d .git ]; then
                uv run -- pre-commit install -t pre-commit -t pre-push >/dev/null 2>&1 || true
              fi
            '';
          };
        }
      );

      # `nix flake check` builds the wrapped package on the current system.
      checks = forAllSystems (system: {
        default = self.packages.${system}.default;
      });
    };
}
