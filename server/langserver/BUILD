load("@io_bazel_rules_go//go:def.bzl", "go_library")

go_library(
    name = "go_default_library",
    srcs = [
        "types.go",
        "langserver.go",
    ],
    deps = [
        "//server/config:go_default_library",
        "@com_github_sourcegraph_jsonrpc2//:go_default_library",
    ],
    visibility = ["//visibility:public"],
)
