{
  description = "mimori - local coding-agent state service";
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  outputs = { self, nixpkgs }:
    let
      systems = [ "aarch64-darwin" "x86_64-linux" "aarch64-linux" ];
      eachSystem = nixpkgs.lib.genAttrs systems;
    in {
      packages = eachSystem (system:
        let pkgs = import nixpkgs { inherit system; };
        in {
          default = pkgs.buildGoModule {
            pname = "mimori";
            version = "0.1.0";
            src = self;
            vendorHash = "sha256-P7Viwr2hrzrVCac04v9NNePGgLsXQfiEoMe3nIVjkio=";
            subPackages = [ "cmd/mimori" ];
            doCheck = true;
            checkPhase = "go test ./...";
          };
        });
      checks = eachSystem (system: { package = self.packages.${system}.default; });
      devShells = eachSystem (system:
        let pkgs = import nixpkgs { inherit system; };
        in { default = pkgs.mkShell { packages = [ pkgs.go pkgs.gopls pkgs.python3 ]; }; });
    };
}
