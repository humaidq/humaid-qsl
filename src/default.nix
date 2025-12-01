# Copyright 2025 Humaid Alqasimi
# SPDX-License-Identifier: Apache-2.0
{ pkgs, ... }:

pkgs.buildGoModule {
  pname = "humaid-qsl";
  version = "0.1.0";

  src = ./.;

  # The vendor hash for Go dependencies
  vendorHash = "sha256-veFMlmcf9VrpmvbPom7Y2FblDYIkgpTv6w5Xpzchh9o=";

  # Build from the src directory
  subPackages = [ "." ];

  meta = with pkgs.lib; {
    description = "Humaid's QSL log";
    homepage = "https://github.com/humaidq/humaid-qsl";
    license = licenses.asl20;
    maintainers = [ ];
  };
}
