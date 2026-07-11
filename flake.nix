{
  description = "dantofa platform — the dctl control CLI, packaged with its runtime CLIs (kind/flux/docker/git).";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";
  };

  outputs =
    { self, nixpkgs }:
    let
      inherit (nixpkgs) lib;

      # A sandboxed Nix build has no .git, so we derive the version purely from
      # the flake source: the commit date as a PEP 440-style dev segment plus the
      # short rev. Downstream tracks this flake by rev (e.g. a Nix input on
      # `master` that `nix flake update` locks to the latest SHA), so
      # `dctl --version` names the exact commit and changes when they update.
      # There are no release tags in this model; a flake cannot read git tags.
      version =
        "0.0.0.dev"
        + (builtins.substring 0 8 (self.lastModifiedDate or "00000000"))
        + "+g"
        + (self.shortRev or self.dirtyShortRev or "dev");

      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      forAllSystems = lib.genAttrs systems;
      pkgsFor = system: nixpkgs.legacyPackages.${system};

      # Dev-shell packages come from the same pinned nixpkgs as the package (one
      # resolver, one lockfile). bws is unfree, so allow exactly that one package
      # rather than opening allowUnfree globally.
      devPkgsFor =
        system:
        import nixpkgs {
          inherit system;
          config.allowUnfreePredicate = pkg: builtins.elem (lib.getName pkg) [ "bws" ];
        };

      # External CLIs the platform shells out to, bundled into the package closure
      # so Nix consumers need no separate install. git is included for the
      # OCI-stamp provenance read (HEAD / remote / branch / dirty).
      runtimeTools = pkgs: [
        pkgs.kind
        pkgs.fluxcd
        pkgs.docker-client
        pkgs.git
      ];

      dctlFor =
        system:
        let
          pkgs = pkgsFor system;
        in
        pkgs.buildGoModule {
          pname = "dctl";
          inherit version;
          src = ./.;
          # Recompute with `just update` / after changing go.sum. Set to
          # lib.fakeHash and rebuild to learn the new value.
          vendorHash = "sha256-VNmPNhs1qvBvyULehp4gwAdjqytzFcKdw504YkfFtv8=";

          subPackages = [ "cmd/dctl" ];
          env.CGO_ENABLED = "0";

          # Stamp the source-derived version into the binary; -s -w drop debug
          # info for a smaller closure.
          ldflags = [
            "-s"
            "-w"
            "-X github.com/dantofa/platform/internal/version.Version=${version}"
          ];

          # Wrap dctl so kind/flux/docker/git travel in its runtime closure —
          # that is what spares Nix consumers a separate tool install.
          nativeBuildInputs = [ pkgs.makeWrapper ];
          postInstall = ''
            wrapProgram "$out/bin/dctl" \
              --prefix PATH : ${lib.makeBinPath (runtimeTools pkgs)}
          '';

          meta = {
            description = "dantofa platform control CLI (bundled with kind/flux/docker/git).";
            mainProgram = "dctl";
          };
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
          # The development environment. Generic dev/CI tooling from the flake's
          # pinned nixpkgs, plus the runtime CLIs shared with the package via
          # `runtimeTools` — so a `go run ./cmd/dctl` / `just run` shells out to
          # the same kind/flux/docker/git the packaged dctl bundles. Enter with
          # `nix develop` (or `direnv`).
          default = pkgs.mkShell {
            packages = (runtimeTools pkgs) ++ [
              # Go toolchain + analyzers.
              pkgs.go
              pkgs.gopls
              pkgs.golangci-lint
              pkgs.gofumpt
              pkgs.govulncheck
              # Generic env tooling.
              pkgs.just
              pkgs.actionlint
              pkgs.yamllint
              pkgs.gh
              pkgs.ratchet
              pkgs.shellcheck
              pkgs.pre-commit
              pkgs.bws
              pkgs.kubectl
            ];
            # Local kubeconfig target.
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
                pre-commit install -t pre-commit -t pre-push >/dev/null 2>&1 || true
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
