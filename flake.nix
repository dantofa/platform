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
      # the flake source: a CalVer date (the commit's, YYYY.MM.DD) plus the short
      # rev as build metadata — the two things that actually identify an untagged
      # commit. Downstream tracks this flake by rev (a Nix input on `master` that
      # `nix flake update` locks to the latest SHA), so `dctl --version` names the
      # exact commit and changes when they update. There are no release tags in
      # this model; a flake cannot read git tags. (Real semver arrives with the
      # operator phase, where container tags / CRD versions force it.)
      version =
        let
          d = self.lastModifiedDate or "00000000";
        in
        "${builtins.substring 0 4 d}.${builtins.substring 4 2 d}.${builtins.substring 6 2 d}"
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

      # Browser tooling for the cluster.just `local-chromium` / `local-playwright`
      # recipes (a browser wired to the local ingress via --host-resolver-rules).
      # Deliberately NOT in runtimeTools (dctl never shells out to these) — they live
      # in the dev shell and the downstream cluster-toolchain only. Playwright also
      # needs PLAYWRIGHT_BROWSERS_PATH pointed at playwright-driver.browsers (set in
      # the dev-shell env and the devbox plugin) so it uses the Nix-provided browsers
      # instead of downloading its own, which don't run on Nix.
      browserTools = pkgs: [
        pkgs.chromium
        pkgs.nodejs
        pkgs.playwright-driver.browsers
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
          # Recomputed automatically by `just vendor-hash` (which `just update`
          # runs); run that standalone after any other go.sum change.
          vendorHash = "sha256-qDGo52n8JSz6PsJWvqTtisOWxB7QTt0mxqxD9+Z/8Dc=";

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
        # The reusable cluster-ops justfile module + the base accepted-CVE list, so
        # a downstream repo (or the devbox plugin) can materialize and import them,
        # rev-pinned via its flake.lock. Trivial text derivations — they do not
        # touch the dctl package, its vendorHash, or the version assertion.
        cluster-just = (pkgsFor system).writeTextFile {
          name = "cluster-just";
          destination = "/cluster.just";
          text = builtins.readFile ./cluster.just;
        };
        # Non-dot destination: a leading-dot filename does not survive the Nix
        # profile merge devbox uses, so the devbox plugin's init_hook copies this
        # to ./.just/.trivyignore-base (adding the dot itself).
        trivyignore-base = (pkgsFor system).writeTextFile {
          name = "trivyignore-base";
          destination = "/trivyignore-cluster";
          text = builtins.readFile ./.trivyignore-cluster;
        };
        # The downstream-agent skill: the platform's conventions, infrastructure, and
        # primitives, so Claude agents on consuming projects reuse the platform instead
        # of rebuilding it. The devbox plugin materializes it into .claude/skills for
        # auto-discovery. Trivial text derivation (no dctl/vendorHash impact).
        skills = (pkgsFor system).writeTextFile {
          name = "dantofa-platform-skill";
          destination = "/SKILL.md";
          text = builtins.readFile ./SKILLS.md;
        };
        # The init_hook helper that gives a consuming project a justfile importing
        # cluster.just. A script rather than another inline init_hook one-liner: it
        # has real edge cases (which filename just picks up, appending to a file with
        # no trailing newline, staying idempotent), so it lives in devbox/ where
        # `just lint` shellchecks it and devbox/smoke.sh unit-tests both paths. The
        # plugin runs it straight out of $DEVBOX_PACKAGES_DIR, so nothing extra lands
        # in the consumer's tree. Trivial text derivation (no dctl/vendorHash impact).
        ensure-justfile = (pkgsFor system).writeTextFile {
          name = "ensure-justfile";
          destination = "/ensure-justfile.sh";
          text = builtins.readFile ./devbox/ensure-justfile.sh;
          executable = true;
        };
        # The CLIs the cluster.just recipes shell out to (dctl comes from
        # `default`), pinned to the same nixpkgs as everything else — bundled for
        # downstream consumers via the devbox plugin so `just cluster …` runs
        # without a separate install.
        cluster-toolchain =
          let
            pkgs = pkgsFor system;
          in
          pkgs.buildEnv {
            name = "cluster-toolchain";
            paths = [
              pkgs.just
              pkgs.kubectl
              pkgs.velero
              pkgs.jq
              pkgs.fluxcd
            ] ++ (browserTools pkgs);
          };
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
            packages = (runtimeTools pkgs) ++ (browserTools pkgs) ++ [
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
              pkgs.shellcheck
              pkgs.pre-commit
              pkgs.bws
              pkgs.kubectl
              # jq for the CI integration checks that parse kubectl JSON (e.g. the
              # trivy-operator image-scan gate); CI only, not a runtimeTool.
              pkgs.jq
              # velero CLI for the backup-verification integration checks (CI
              # only; dctl never shells out to it, so it is not a runtimeTool).
              pkgs.velero
            ];
            # Claude configuration
            CLAUDE_CONFIG_DIR = ".claude";
            # Local kubeconfig target.
            KUBECONFIG = ".kubeconfig";
            # In-repo dev runs dctl from source: cluster.just defaults $DCTL to
            # `nix run …#default --` (for downstream), so override it here to the
            # working tree, so `just cluster local …` never runs a stale artifact.
            DCTL = "go run ./cmd/dctl";
            # Point Playwright (npx or nixpkgs) at the Nix-provided browsers rather
            # than its own download (which isn't runnable on Nix); skip the host-lib
            # check since those deps come from Nix, not the host distro. Downstream's
            # @playwright/test version should match this playwright-driver.
            PLAYWRIGHT_BROWSERS_PATH = "${pkgs.playwright-driver.browsers}";
            PLAYWRIGHT_SKIP_VALIDATE_HOST_REQUIREMENTS = "true";
            shellHook = ''
              # Put `just build` output on PATH so the fast, cache-backed local
              # build is callable as `dctl` (the intended entrypoint) without a
              # from-scratch `nix run .#default`. The runtime CLIs the package
              # wraps in are already on PATH here, so dist/dctl behaves the same.
              export PATH="$PWD/dist:$PATH"
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
