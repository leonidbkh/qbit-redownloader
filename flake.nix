{
  description = "qbit-redownloader — replace stale rutracker torrents in qBittorrent";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
      in
      {
        packages.default = pkgs.buildGoModule {
          pname = "qbit-redownloader";
          version = "0.1.0";
          src = ./.;
          vendorHash = "sha256-g+yaVIx4jxpAQ/+WrGKxhVeliYx7nLQe/zsGpxV4Fn4=";
          meta = with pkgs.lib; {
            description = "Detects stale rutracker torrents in qBittorrent and replaces them via Prowlarr";
            license = licenses.mit;
            mainProgram = "qbit-redownloader";
          };
        };

        devShells.default = pkgs.mkShell {
          packages = [ pkgs.go ];
        };
      });
}
