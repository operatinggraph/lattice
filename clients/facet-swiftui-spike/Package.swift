// swift-tools-version:5.9
import PackageDescription

let package = Package(
    name: "FacetSwiftUISpike",
    platforms: [.macOS(.v13)],
    products: [
        .library(name: "FacetManifestKit", targets: ["FacetManifestKit"]),
    ],
    targets: [
        .target(name: "FacetManifestKit", path: "Sources/FacetManifestKit"),
        .executableTarget(
            name: "FacetSwiftUISpike",
            dependencies: ["FacetManifestKit"],
            path: "Sources/FacetSwiftUISpike"
        ),
        .testTarget(
            name: "FacetManifestKitTests",
            dependencies: ["FacetManifestKit"],
            path: "Tests/FacetManifestKitTests"
        ),
    ]
)
