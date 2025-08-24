#!/usr/bin/env python3

"""
Kuba is the magical kubectl companion.

Kuba provides a user-friendly interface to a mostly read-only subset of kubectl commands.
"""

import inspect
import json
import os
import re
import subprocess
import sys
from collections import defaultdict
from dataclasses import dataclass
from difflib import SequenceMatcher
from enum import Enum
from functools import lru_cache
from itertools import chain, product
from pathlib import Path
from types import FrameType
from typing import (
    IO,
    Any,
    Callable,
    Iterable,
    Iterator,
    NamedTuple,
    Optional,
    Union,
    cast,
    overload,
)

import click
import click.shell_completion
import yaml
from coda import getenv_bool

DEBUG = getenv_bool("KUBA_DEBUG") or False
TRACE = getenv_bool("KUBA_TRACE") or False

BOLD = "\033[1m"
ITALIC = "\033[3m"
UNDERLINE = "\033[4m"
BLACK = "\033[30m"
WHITE = "\033[97m"
GRAY = "\033[90m"
GREEN = "\033[32m"
YELLOW = "\033[33m"
RED = "\033[91m"
CYAN = "\033[36m"
RESET = "\033[0m"

U_AND = "∧"
U_OR = "∨"
U_EQ = "="
U_NEQ = "≠"
U_LT = "<"
U_LE = "≤"
U_GT = ">"
U_GE = "≥"
U_IN = "∈"
U_NIN = "∉"
U_EX = "∃"
U_NEX = "∄"
U_EMPTY = "∅"

SEP_LEN = 3
SEP = " " * SEP_LEN

FILENAME = __file__

NATIVE_RTYPES = {
    "c": "configmap",
    "d": "deployment",
    # NOTE: e reserved for exec
    "j": "job",
    # NOTE: l reserved for logs
    "m": "daemonset",
    "n": "node",
    "o": "cronjob",
    "p": "pod",
    "q": "replicaset",
    "r": "secret",
    "s": "service",
    "x": "lease",
}


def log(msg: str, debug: bool):
    if debug:
        click.echo(f"{msg}", err=True)


trace_depth = 0


def trace_fn(frame: FrameType, event: str, arg: any) -> Optional[Callable]:  # intentionally mutable default
    global trace_depth  # HACK: best way to handle indentation tracking
    filename = frame.f_code.co_filename
    if filename != FILENAME:
        return
    func = frame.f_code.co_name

    skip_funcs = [
        "trace_fn",
        "__str__",
        "__repr__",
        "log",
        "log_color",
        "print_color",
        "colorize",
        "color",
        "is_subseq",
        "ansi_len",
        "ansi_ljust",
        "strip_ansi",
    ]
    if func in skip_funcs or func.startswith("<"):  # ignore unhelpful and synthetic functions
        return

    if event == "call":
        args = inspect.formatargvalues(*inspect.getargvalues(frame))
        log(f"{BLACK}{'|   ' * trace_depth}{RESET}{RED}{func} {args}{RESET}", True)
        trace_depth += 1
    if event == "return":
        trace_depth -= 1
        log(f"{BLACK}{'|   ' * trace_depth}{RESET}{BLACK}{func} => {arg}{RESET}", True)
    return trace_fn


if TRACE:
    sys.setprofile(trace_fn)


class ColorizedClickException(click.ClickException):
    def __init__(self, msg: str):
        super().__init__(colorize(f"{RED}{msg}{RESET}", out="stderr"))


class NoMatchException(ColorizedClickException):
    pass


PAIRS = (
    (U_AND, U_OR),
    (U_EQ, U_NEQ),
    (U_LT, U_GE),
    (U_LE, U_GT),
    (U_IN, U_NIN),
    (U_EX, U_NEX),
)
NEG = {k: v for a, b in PAIRS for k, v in ((a, b), (b, a))}


def op(u: str, negate: bool) -> str:
    if not negate:
        return u
    return NEG[u]


class Select(Enum):
    YES = True  # select if multiple results
    UNSET = False  # no preference
    ONE = "one"  # YES but restrict to at most one option
    ANY = "any"  # auto-select one option if multiple results
    ALL = "all"  # select all options if multiple results

    def __bool__(self) -> bool:
        return bool(self.value)

    def is_selective(self) -> bool:
        return self in (Select.YES, Select.ANY, Select.ONE)

    def with_one(self, one: bool) -> "Select":
        if self == Select.ANY:
            return self
        return Select.ONE if one else self

    def with_n_resources(self, n: int) -> "Select":
        return self.yessify() if n > 0 else self

    def yessify(self) -> "Select":
        return self or Select.YES


@dataclass
class Resource:
    name: str = ""
    namespace: str = ""
    cluster: str = ""
    description: str = ""  # full-line object description from kubectl get

    Indices: type = Optional[tuple[int, int, int]]  # (name, namespace, cluster)

    @classmethod
    def from_description(
        cls, description: str, desc_has_namespace: bool, desc_has_cluster: bool, indices: "Resource.Indices", *, cluster: str = "", namespace: str = ""
    ) -> "Resource":
        """
        Extract resource from a full resource description.

        Name and namespace are plain text, description is the full line from kubectl get (potentially colorized).

        Custom indices can be provided to extract the (name, namespace) fields from the description.

        Scenarios:
        - empty => empty
        - all_namespaces=False => name is 1st field
        - all_namespaces=True => namespace is 1st field, name is 2nd field
        - indices => extract fields using indices as (name, namespace)
        """
        r = description.split()
        if not indices:
            indices = get_default_indices(len(r), desc_has_namespace, desc_has_cluster)
        name = r[indices[0]] if indices[0] >= 0 else ""
        namespace = r[indices[1]] if indices[1] >= 0 else namespace
        cluster = r[indices[2]] if indices[2] >= 0 else cluster
        return cls(strip_ansi(name), strip_ansi(namespace), strip_ansi(cluster), description)

    def __init__(self, name: str, namespace: str, cluster: str, description: str):
        self.name = name
        self.namespace = namespace
        self.cluster = cluster
        self.description = description

    def __str__(self) -> str:
        try:
            return f"{self.cluster or '<none>'}/{self.namespace or '<none>'}/{self.name or '<none>'}"
        except AttributeError:
            return "<empty>"

    def __repr__(self) -> str:
        return f"Resource({self.name=}, {self.namespace=}, {self.cluster=})"

    def __eq__(self, other) -> bool:
        if isinstance(other, Resource):
            return self.namespace == other.namespace and self.name == other.name
        if isinstance(other, str):
            return str(self) == other
        return False


def get_default_indices(n: int, all_namespaces: bool, multi_cluster: bool) -> Resource.Indices:
    # (name, namespace, cluster)
    if not n:
        return -1, -1, -1
    if n == 1:
        return 0, -1, -1
    if n == 2:
        if all_namespaces and multi_cluster:
            raise ValueError(f"unexpected number of fields for multi-cluster and all-namespaces: {n}")
        elif all_namespaces:
            return 1, 0, -1
        elif multi_cluster:
            return 1, -1, 0
        else:
            return 0, -1, -1
    return {
        (True, True): (2, 1, 0),
        (False, True): (1, -1, 0),
        (True, False): (1, 0, -1),
        (False, False): (0, -1, -1),
    }[(all_namespaces, multi_cluster)]


@dataclass
class Resources(list[Resource]):
    header: str = ""
    indices: Resource.Indices = ()
    sentinel: bool = False

    @classmethod
    def from_descriptions(
        cls,
        descriptions: list[str],
        desc_has_namespace: bool,
        desc_has_cluster: bool,
        header: Union[bool, str],
        indices: Resource.Indices,
        *,
        namespace: str = "",
        cluster: str = "",
    ) -> "Resources":
        if type(header) not in (bool, str):
            raise ValueError("header must be a string or a boolean")
        if not header:
            header = ""
        elif header is True:
            header = descriptions.pop(0) if descriptions else ""
        return cls(
            descriptions, header, indices, desc_has_namespace=desc_has_namespace, desc_has_cluster=desc_has_cluster, namespace=namespace, cluster=cluster
        )

    @classmethod
    def from_clusters(cls, cluster_to_resources: dict[str, "Resources"], kubectl: str, debug: bool) -> "Resources":
        if not cluster_to_resources:
            raise ValueError("expected at least one cluster")
        if len(cluster_to_resources) == 1:
            return cluster_to_resources.popitem()[1]

        headers = remove_empty({tuple(rs.header.split()) for rs in cluster_to_resources.values()})
        if len(headers) > 1:
            raise ValueError(f"expected all resources to have the same header, got: {headers}")
        header = get_any_val([rs.header for rs in cluster_to_resources.values()])
        indices = get_only_val([rs.indices for rs in cluster_to_resources.values()])
        if indices:
            raise NotImplementedError("indices not supported for cluster resources")
        sentinel = get_only_val([rs.sentinel for rs in cluster_to_resources.values()])

        header = colorize(f"{BOLD}CLUSTER{RESET}{SEP}{header}", kubectl=kubectl) if header else ""
        for c, rs in cluster_to_resources.items():
            rs.header = header
            for r in rs:
                r.description = colorize(f"{CYAN}{c}{RESET}{SEP}{r.description}", kubectl=kubectl) if r.description else ""

        return Resources(list(chain.from_iterable(cluster_to_resources.values())), header, indices, sentinel=sentinel).justify(debug)

    def __init__(
        self,
        resources: list[Union[str, Resource]],
        header: str,
        indices: Resource.Indices,
        *,
        cluster: str = "",
        namespace: str = "",
        desc_has_namespace: bool = False,
        desc_has_cluster: bool = False,
        sentinel: bool = False,
    ):
        self.header = header
        self.indices = indices
        self.sentinel = sentinel
        if all(isinstance(r, str) for r in resources):
            super().__init__(
                [Resource.from_description(r, desc_has_namespace, desc_has_cluster, indices, cluster=cluster, namespace=namespace) for r in resources]
            )
        elif all(isinstance(r, Resource) for r in resources):
            super().__init__(resources)
        else:
            raise ValueError("resources must be all strings or all Resource objects")

    def __str__(self) -> str:
        return ", ".join(str(r) for r in self) or "<empty>"

    def __repr__(self) -> str:
        return f"Resources({', '.join([repr(r) for r in self])})"

    def filter(self, include: Callable[[Resource], bool]) -> "Resources":
        return Resources([r for r in self if include(r)], self.header, self.indices)

    def filter_by_name(self, name: str) -> "Resources":
        return self.filter(lambda r: r.name == name)

    def filter_by_descriptions(self, descriptions: list[str]) -> "Resources":
        """
        Filter resources by their full-line descriptions.

        Assumes input descriptions are plain text (no ANSI escape codes).

        Useful because e.g. fzf unconditionally strips ANSI escape codes from its output.
        """
        return self.filter(lambda r: strip_ansi(r.description) in descriptions)

    def filter_to_one(self) -> "Resources":
        if not self:
            raise ValueError("no resources to pick from")
        return self.filter(lambda r: r.name == self[0].name and r.namespace == self[0].namespace)

    def names(self) -> list[str]:
        return [r.name for r in self]

    def namespaces(self) -> list[str]:
        return sorted({r.namespace for r in self})

    def clusters(self) -> list[str]:
        return sorted({r.cluster for r in self})

    def descriptions(self) -> list[str]:
        return [r.description for r in self]

    def by_namespace(self) -> dict[str, "Resources"]:
        return {ns: self.filter(lambda r: r.namespace == ns) for ns in self.namespaces()}

    def by_cluster(self) -> dict[str, "Resources"]:
        return {c: self.filter(lambda r: r.cluster == c) for c in self.clusters()}

    def by_cluster_by_namespace(self) -> dict[str, dict[str, "Resources"]]:
        return {c: rs.by_namespace() for c, rs in self.by_cluster().items()}

    def justify(self, debug: bool, *, rectify: Callable[[list[str]], None] = None) -> "Resources":
        """Justify resources from different clusters into a single table."""
        if not self:
            raise ValueError("expected at least one resource")

        r = re.compile(rf"\s{{{SEP_LEN},}}")  # e.g. \s{3,}

        rows = [self.header] + [r.description.strip() for r in self]
        row_cols = [r.split(row) for row in rows]

        if rectify:
            for row in row_cols:
                rectify(row)

        n_cols = len(row_cols[0])
        if not all(len(row) == n_cols for row in row_cols):
            for idx, row in enumerate(row_cols):
                log(f"row {idx}: {row}", debug)
            raise ValueError(f"expected all rows to have {n_cols} columns, got: {[len(row) for row in row_cols]}")

        col_widths = [max(ansi_len(row[c]) for row in row_cols) for c in range(n_cols)]
        justified_rows = [SEP.join(ansi_ljust(row[c], col_widths[c]) for c in range(n_cols)) for row in row_cols]
        justified_header = justified_rows.pop(0)

        self.header = justified_header
        for resource, justified_description in zip(self, justified_rows):
            resource.description = justified_description

        return self


def get_any_val[T](it: Iterable[T]) -> T:
    """
    Get any value from an iterable.

    Raise an exception if the iterable is empty.
    """
    try:
        return next(iter(it))
    except StopIteration:
        raise ValueError("expected at least one value")


def get_only_val[T](it: Iterable[T]) -> T:
    """
    Get the only value from an iterable.

    Raise an exception if there is not exactly one repeated value, or if the iterable is empty.
    """
    vals = set(it)
    if len(vals) != 1:
        raise ValueError(f"expected exactly one value, got {len(vals)}: {vals}")
    return vals.pop()


@dataclass
class Command(list):
    namespace: str = ""
    cluster: str = ""

    def __init__(self, args: list[str], namespace: Optional[str], cluster: Optional[str]):
        super().__init__(args)
        self.namespace = namespace or ""
        self.cluster = cluster or ""

    def __repr__(self) -> str:
        return f"Command({self.cluster=}, {self.namespace=}, {' '.join(self)})"

    def run(self, just_print: bool, kubectl: str, *, stderr_only: bool = False):
        if just_print:
            self.print()
        else:
            self.execute(kubectl, stderr_only=stderr_only)

    def execute(
        self, kubectl: str, *, print_ns: bool = False, print_cluster: bool = False, err_msg: str = None, stderr_only: bool = False, raise_errs: bool = True
    ):
        if print_cluster:
            print_color(f"{BOLD}CLUSTER{RESET} {CYAN}{self.cluster}{RESET}", kubectl, flush=True)
        if print_ns:
            print_color(f"{BOLD}NAMESPACE{RESET} {CYAN}{self.namespace}{RESET}", kubectl, flush=True)
        try:
            if stderr_only:
                subprocess.run(self, check=True, stdout=sys.stderr)
            else:
                subprocess.run(self, check=True)
        except subprocess.CalledProcessError as e:
            if raise_errs:
                raise ColorizedClickException(f"{err_msg or 'failed to run command'}: {e}")
        return None

    def print(self):
        print(" ".join(self))


@dataclass
class Commands(list[Command]):
    def __init__(self, commands: list[Command]):
        super().__init__(commands)

    def __repr__(self) -> str:
        return f"Commands({' ### '.join([repr(c) for c in self])})"

    def run(
        self,
        cmd_name: str,
        just_print: bool,
        kubectl: str,
        *,
        print_namespaces: bool = False,
        print_clusters: bool = False,
        must_single_command: str = "",
        stderr_only: bool = False,
    ):
        if not self:
            raise ColorizedClickException("no commands generated")
        n_namespaces = len(self.namespaces())
        n_clusters = len(self.clusters())
        if must_single_command and (n_namespaces > 1 or n_clusters > 1):
            raise ColorizedClickException(
                f"generated {len(self)} commands due to {n_namespaces} namespaces and/or {n_clusters} clusters, won't run with more than 1 due to {must_single_command}"
            )
        if just_print:
            self.print()
        else:
            self.execute(cmd_name, kubectl, print_namespaces, print_clusters, stderr_only=stderr_only)

    def execute(self, cmd_name: str, kubectl: str, print_namespaces: bool, print_clusters: bool, *, stderr_only: bool = False):
        for idx, cmd in enumerate(self):
            multi_msg = f" (command {idx+1}/{len(self)})" if self.is_multi() else ""
            err_msg = f"failed to run {cmd_name}{multi_msg}"
            cmd.execute(
                kubectl, err_msg=err_msg, print_ns=print_namespaces, print_cluster=print_clusters, stderr_only=stderr_only, raise_errs=not print_clusters
            )
            if self.is_multi() and idx < len(self) - 1:
                print(flush=True)

    def print(self):
        if len(self) > 1:
            raise ColorizedClickException(
                f"generated {len(self)} commands under {len(self.clusters())} clusters and {len(self.namespaces())} namespaces, won't print with more than 1 command"
            )
        self[0].print()

    def namespaces(self) -> list[str]:
        return sorted({c.namespace for c in self})

    def clusters(self) -> list[str]:
        return sorted({c.cluster for c in self})

    def is_multi(self) -> bool:
        return len(self) > 1


def tpl_zsh_alias(alias: str, debug: bool, *, rtypes: dict[str, str] = None) -> str:
    tpl = """
function _{alias} {{
  words=(kuba {subcommand} $words[2,-1])
  CURRENT=$((CURRENT+{args}))
  _kuba_completion
}}
compdef _{alias} {alias}
""".strip()
    subcommands = {
        "l": ["logs"],
        "e": ["exec"],
    }
    if rtypes:
        resource_action = "describe" if alias[-1] == "d" else "get"
        subcommands.update({char: [resource_action, rtype] for char, rtype in rtypes.items()})

    subcommand = subcommands[alias[1]]
    if "a" in alias[2:]:
        subcommand.append("--all-namespaces")
    if "k" in alias[2:]:
        subcommand.append("--namespace kube-system")
    if "x" in alias[2:]:
        subcommand.append("--leader")

    args = len(subcommand)
    subcommand = " ".join(subcommand)
    log(f"tpl_zsh_alias: {alias=}, {subcommand=}, {args=}", debug)
    return tpl.format(alias=alias, subcommand=subcommand, args=args)


def get_shell() -> str:
    shell = os.getenv("SHELL", "")
    log(f"get_shell: {shell=}", DEBUG)
    for supported_shell in ("zsh",):
        if supported_shell in shell:
            return supported_shell
    return ""


def parse_kv(_: Optional[click.Context], __: Optional[click.Parameter], value: str) -> dict[str, str]:
    """
    Parse a map-like string into a string->string dictionary.

    Format example: k1=v1,k2=v2
    """
    resources = dict()
    if not value:
        return resources
    pairs = value.split(",")
    for pair in pairs:
        if "=" not in pair:
            raise click.BadParameter(f"invalid resource mapping '{pair}': expected format key=value")
        key, val = pair.split("=", 1)
        key = key.strip()
        val = val.strip()
        if not key or not val:
            raise click.BadParameter(f"invalid resource mapping '{pair}': key and value must be non-empty")
        resources[key] = val
    return resources


def parse_kv_simple_regex(_: Optional[click.Context], __: Optional[click.Parameter], value: str) -> dict[re.Pattern, str]:
    """
    Parse a map-like string into a regex->string dictionary.

    Supported wildcard characters:
    - * => zero or more characters
    - | => or

    Format example: k1*=v1,k|2=v2
    """
    kv = dict()
    for k_simple, k_regex, v in transform_simple_regexes(parse_kv(_, __, value)):
        try:
            k = re.compile(k_regex)
        except re.error as e:
            raise click.BadParameter(f"invalid regex '{k_simple}', which was transformed to '{k_regex}': {e}")
        kv[k] = v
    return kv


ClusterAliases = dict[str, str]
ClusterGroups = dict[str, tuple[str, ...]]


def parse_cluster_aliases(_: Optional[click.Context], __: Optional[click.Parameter], value: str) -> ClusterAliases:
    return _parse_clusters(_, __, value)[0]


def parse_cluster_groups(_: Optional[click.Context], __: Optional[click.Parameter], value: str) -> ClusterGroups:
    return _parse_clusters(_, __, value)[1]


def _parse_clusters(ctx: Optional[click.Context], __: Optional[click.Parameter], value: str) -> tuple[ClusterAliases, ClusterGroups]:
    """
    Parse a map-like string into a string->list[string] dictionary.

    Supported wildcard characters:
    - * => zero or more characters

    Format example:
    - dev=cmp-test,dev*=fed-cmp-test-*
    - With clusters cmp-test, fed-cmp-test-1a, fed-cmp-test-1b
    Would be parsed to e.g.:
    - Cluster aliases: {kdev: cmp-test, kdev1a: fed-cmp-test-1a, kdev1b: fed-cmp-test-1b}
    - Cluster groups (notional): {{cmp-test}, {fed-cmp-test-1a, fed-cmp-test-1b}}
    """
    if not ctx:
        return ClusterAliases(), ClusterGroups()

    kv = parse_kv(None, None, value)
    if not kv:
        return ClusterAliases(), ClusterGroups()
    for k in kv:
        if k.count("*") > 1:
            raise click.BadParameter(f"invalid cluster alias '{k}': only one wildcard character '*' allowed")

    contexts = get_contexts(ctx.params.get("kubectl", "kubectl"), "", DEBUG).names()
    if not contexts:
        print("kuba: error parsing kubeconfig contexts: no contexts found", file=sys.stderr)
        return ClusterAliases(), ClusterGroups()

    aliases = ClusterAliases()
    groups = ClusterGroups()
    for k, v in kv.items():
        r = re.compile(transform_simple_regex(v, allow_pipe=False, capture_star=True, full_match=True))
        group = []
        for c in contexts:
            if match := re.search(r, c):
                aliases[k.replace("*", "".join(match.groups()))] = c
                group.append(c)
        for c in group:
            groups[c] = tuple(group)

    return aliases, groups


def transform_simple_regexes(kv: dict[str, str]) -> list[tuple[str, str, str]]:
    items = []
    for k, v in kv.items():
        kk = transform_simple_regex(k)
        items.append((k, kk, v))
    return items


def transform_simple_regex(k: str, *, allow_star: bool = True, allow_pipe: bool = True, capture_star: bool = False, full_match: bool = False) -> str:
    transforms = [
        lambda x: x.replace("*", ".*" if allow_star else ""),
        lambda x: x.replace("|", "$|^" if allow_pipe else ""),
        lambda x: x.replace(".*", "(.*)") if capture_star else x,
        lambda x: f"^{x}$" if full_match else x,
    ]
    for transform in transforms:
        k = transform(k)
    return k


def complete_rtype_fullname(ctx: click.Context, _: click.Parameter, incomplete: str) -> list[str]:
    """Shell completion for `kuba get` type argument."""
    log(f"{ctx.args=}, {ctx.params=}, {incomplete=}", DEBUG)
    try:
        return get_resource_types(ctx.params.get("kubectl", "kubectl"), incomplete, DEBUG)
    except click.ClickException:
        return []


def complete_rtype(ctx: click.Context, _: click.Parameter, incomplete: str) -> list[str]:
    """Shell completion for `kuba get` type argument."""
    log(f"{ctx.args=}, {ctx.params=}, {incomplete=}", DEBUG)
    try:
        return get_api_resources(ctx.params.get("kubectl", "kubectl"), incomplete, DEBUG).names()
    except click.ClickException:
        return []


def complete_resource(ctx: click.Context, _: click.Parameter, incomplete: str) -> list[str]:
    """Shell completion for `kuba get` resource argument."""
    log(f"{ctx.args=}, {ctx.params=}, {incomplete=}", DEBUG)
    if ctx.params.get("all_namespaces"):
        return []
    try:
        return get_resources(
            ctx.params.get("kubectl", "kubectl"),
            ctx.params.get("context"),
            ctx.params["rtype"],
            incomplete,
            ctx.params["namespace"],
            ctx.params["all_namespaces"],
            [],
            ctx.params["label"],
            "",
            DEBUG,
            leader=ctx.params["leader"],
        ).names()
    except click.ClickException:
        return []


def complete_context(ctx: click.Context, _: click.Parameter, incomplete: str) -> list[str]:
    """Shell completion for `kuba ctx` context argument."""
    log(f"{ctx.args=}, {ctx.params=}, {incomplete=}", DEBUG)
    try:
        return get_contexts(ctx.params.get("kubectl", "kubectl"), incomplete, DEBUG).names()
    except click.ClickException:
        return []


def complete_namespace(ctx: click.Context, _: click.Parameter, incomplete: str) -> list[str]:
    """Shell completion for `kuba ns` namespace argument."""
    log(f"{ctx.args=}, {ctx.params=}, {incomplete=}", DEBUG)
    kubectl = ctx.params.get("kubectl", "kubectl")
    context = ctx.params.get("context")
    if not context:
        xquery = ctx.params.get("xquery", "")
        if matching := get_contexts(kubectl, xquery, DEBUG).filter_by_name(xquery):
            context = matching.pop().name
    try:
        return get_resources(
            kubectl,
            context,
            "namespace",
            incomplete,
            "",
            False,
            [],
            "",
            "",
            DEBUG,
        ).names()
    except click.ClickException:
        return []


def complete_ssh(ctx: click.Context, _: click.Parameter, incomplete: str) -> list[str]:
    """Shell completion for `kuba ssh` node argument."""
    log(f"{ctx.args=}, {ctx.params=}, {incomplete=}", DEBUG)
    rtype = "pod" if ctx.params["pods"] else "node"
    try:
        return get_resources(
            ctx.params.get("kubectl", "kubectl"),
            ctx.params.get("context"),
            rtype,
            incomplete,
            "",
            False,
            [],
            ctx.params["label"],
            "",
            DEBUG,
        ).names()
    except click.ClickException:
        return []


def complete_pod(ctx: click.Context, _: click.Parameter, incomplete: str) -> list[str]:
    """Shell completion for `kuba logs` pod argument."""
    log(f"{ctx.args=}, {ctx.params=}, {incomplete=}", DEBUG)
    if ctx.params.get("all_namespaces"):
        return []
    try:
        return get_resources(
            ctx.params.get("kubectl", "kubectl"),
            ctx.params.get("context"),
            "pod",
            incomplete,
            ctx.params["namespace"],
            ctx.params["all_namespaces"],
            [],
            ctx.params["label"],
            "",
            DEBUG,
            leader=ctx.params["leader"],
        ).names()
    except click.ClickException:
        return []


def complete_container(ctx: click.Context, _: click.Parameter, incomplete: str) -> list[str]:
    """Shell completion for `kuba logs` container argument."""
    log(f"{ctx.args=}, {ctx.params=}, {incomplete=}", DEBUG)
    if not ctx.params.get("pod"):
        return []
    try:
        pod = Resource(ctx.params["pod"], ctx.params["namespace"], ctx.params["context"], "")
        return get_containers(ctx.params.get("kubectl", "kubectl"), pod, incomplete, True, DEBUG)
    except click.ClickException:
        return []


@click.group(context_settings=dict(help_option_names=["-h", "--help"], max_content_width=120))
def cli():
    """The magical kubectl companion."""
    pass


@cli.command(name="shellenv")
@click.option(
    "--resources",
    "rtypes",
    callback=parse_kv,
    default=os.getenv("KUBA_SHELLENV_RESOURCES", ""),
    help="Add or override resource mappings, formatted as e.g. p=pod,n=node. Can also set via KUBA_SHELLENV_RESOURCES env var.",
)
@click.option(
    "--clusters",
    "cluster_aliases",
    default=os.getenv("KUBA_CLUSTERS", ""),
    callback=parse_cluster_aliases,
    help="List of clusters to consider, formatted as e.g. k1=v1,k2=v2 (use '*' to match multiple clusters). Can also set via KUBA_CLUSTERS env var.",
)
@click.option("--shell", type=click.Choice(["", "zsh"]), default=get_shell(), help="Override shell detection.")
@click.option(
    "--kubectl", default=os.getenv("KUBA_KUBECTL", "kubectl"), help="Name or path of the kubectl binary to use. Can also set via KUBA_KUBECTL env var."
)
@click.option("--no-native", is_flag=True, help="Don't include the default native resource mappings.")
@click.option("--no-resources", is_flag=True, help="Don't include aliases and completions for resource-level commands.")
@click.option("--no-containers", is_flag=True, help="Don't include aliases and completions for container-level commands.")
@click.option("--ssh-bastion", help="SSH bastion option to use with kuba ssh.")
@click.option("--ssh-use-name", is_flag=True, help="Use name option to use with kuba ssh.")
@click.option("--list", "list_resource_aliases", is_flag=True, help="Just list all resource aliases.")
@click.option("--listq", "list_resource_aliases_query", default="", help="Same as --list, but filter for the query.")
@click.option("--debug", is_flag=True, default=DEBUG, help="Print debug info to stderr.")
def shellenv_cmd(
    rtypes: dict[str, str],
    cluster_aliases: ClusterAliases,
    shell: str,
    kubectl: str,
    no_native: bool,
    no_resources: bool,
    no_containers: bool,
    ssh_bastion: str,
    ssh_use_name: bool,
    list_resource_aliases: bool,
    list_resource_aliases_query: str,
    debug: bool,
):
    """
    Generate k-aliases and completions for easy kubectl resource access.

    E.g. kp for kubectl get pod.

    Generated aliases all forward to kuba, which eventually forwards to kubectl, including any kubectl-specific
    parameters. Passing kubectl arguments and shell completion both have some rough edges.

    GENERAL

    \b
    - kns => kuba ns
    - kctx => kuba ctx (suffixes: l=list, n=ns)
    - kclus => kuba cluster
    - kssh => kuba ssh (suffixes: a=any, p=pods)
    - kapi => kuba api (suffixes: n=name, z=select)
    - ksys => kuba ns kube-system

    RESOURCE-LEVEL

    \b
    Default native resource mappings:
    - c=configmap
    - d=deployment
    - j=job
    - m=daemonset
    - n=node
    - o=cronjob
    - p=pod
    - r=secret
    - s=service
    - x=lease

    \b
    Alias modifiers, optional and in this order:
    - Search in (a)ll namespaces or (k)ube-system namespace
    - Search across (m)ultiple sibling clusters
    - Restrict to objects e(x)clusively holding a lease
    - Force selection using f(z)f or force (p)icking all
    - Choose an alternative output type:
        - (d)escribe
        - (n)ame
        - (w)ide
        - (y)aml
        - (j)son
        - (f)x
        - (e)vents
        - lo(g)s
        - follow (l)ogs
        - lineage downward i.e. (c)hildren
        - lineage (u)pward
        - p(o)ds (only for nodes, shows pods on the node)
        - n(o)de (only for pods, shows node the pod is on)
        - containe(r)s (only for pods, shows containers in the pod)

    \b
    Example alias usage:
    - kp -> kuba get pod
    - kpw -> kuba get pod -o wide
    - kpz  -> kuba get pod (fzf multi-select over all pods)
    - kpzf myapp -> kuba get pod --select -o json | fx (fzf multi-select over all pods fuzzy matching 'myapp')
    - kxd -> kuba describe lease
    - kny -> kuba get node -o yaml
    - kdj -> kuba get deployment -o json
    - ksz -> kuba get service (fzf multi-select over all services)
    - kjf -> kuba get job -o json | fx
    - kcaj -l app=myapp -> kuba get configmap --all-namespaces -o json -l app=myapp

    CONTAINER-LEVEL

    \b
    Default kuba aliases:
    - l=logs
    - e=exec

    \b
    Alias modifiers, optional and in this order:
    - Search in (a)ll namespaces or (k)ube-system namespace
    - Search across (m)ultiple sibling clusters
    - Restrict to objects e(x)clusively holding a lease (via convenience heuristic)
    - Choose (c)ontainers, or automatically pick a(l)l containers
    - Command-specific
        - logs: (f)ollow logs

    \b
    Example alias usage:
    - kl -> kuba logs --guess
    - ke -> kuba exec --guess
    - klx -> kuba logs --leader --guess
    - kexc -> kuba exec --leader
    - klamlf -> kuba logs --all-namespaces --multi-cluster --all --follow

    Usage: source <(kuba shellenv [OPTIONS])
    """
    if list_resource_aliases or list_resource_aliases_query:
        all_rtypes = NATIVE_RTYPES | rtypes | {"e": "exec", "l": "log"}
        rtype_strs = sorted([f"{k}={v}" for k, v in all_rtypes.items()])
        if list_resource_aliases_query:
            rtype_strs = [s for s in rtype_strs if is_subseq(list_resource_aliases_query, s)]
        for s in rtype_strs:
            print(s)
        return

    lines = list(
        chain(
            shellenv_common(shell, kubectl, cluster_aliases, ssh_bastion, ssh_use_name),
            shellenv_kuba(shell),
            [] if no_resources else shellenv_resources(rtypes, shell, no_native, debug),
            [] if no_containers else shellenv_containers(shell, debug),
        )
    )
    lines = [re.sub(r"\s{2,}", " ", line) for line in lines]  # simplest to remove extra spaces here
    print("\n".join(lines))


def shellenv_common(shell: str, kubectl: Optional[str], cluster_aliases: ClusterAliases, ssh_bastion: str, ssh_use_name: bool) -> list[str]:
    tpls_shared = [
        "export KUBA_KUBECTL={kubectl}" if kubectl else "",
        'function klst {{ if [[ "$#" -gt 0 ]] ; then kuba shellenv --listq "$@" ; else kuba shellenv --list ; fi }}',
        "alias knsl='kuba ns --list'",
        "alias kctxl='kuba ctx --list'",
        "alias kapi='kuba api'",
        "alias kapin='kuba api --output=name'",
        "alias kapiz='kuba api --select'",
        "alias kapizn='kuba api --select --output=name'",
        "alias ksys='kuba ns kube-system'",
        "alias ksched='kuba sched'",
        "alias kschedy='kuba sched --pass'",
        "alias kschedn='kuba sched --fail'",
        "alias kschedp='kuba sched --pods'",
        "alias kschedpy='kuba sched --pods --pass'",
        "alias kschedpn='kuba sched --pods --fail'",
    ]
    tpls = {
        "": [
            "# SOURCE: generated by kuba",
            "",
            "# COMMON",
            "",
            *tpls_shared,
            *[f'function k{k} {{{{ kuba cluster {v} "${{{{1:--}}}}" ; }}}}' for k, v in cluster_aliases.items()],
            "alias kns='kuba ns'",
            "alias kctx='kuba ctx'",
            "alias kctxn='kuba ctx --ns'",
            "alias kclus='kuba cluster'",
            "alias kssh='kuba ssh {bastion} {name}'",
            "alias kssha='kuba ssh {bastion} {name} --any'",
            "alias ksshp='kuba ssh {bastion} {name} --pod'",
        ],
        "zsh": [
            "# SOURCE: generated by kuba",
            "# SHELL: zsh",
            "",
            "# COMMON",
            "",
            *tpls_shared,
            "POWERLEVEL9K_KUBECONTEXT_SHOW_ON_COMMAND+='|kuba|kns|kctx|kctxn|kclus|kssh|kssha|ksshp'",
            'function _kuba_history_evl {{ [[ -n "$1" ]] && print -s "$1" && eval "$1" ; }}',
            *[
                f'function k{k} {{{{ c=$(kuba cluster --command-multi {v} "${{{{1:--}}}}") && [[ -n "$c" ]] && _kuba_history_evl "$c" || return 0 ; }}}}'
                for k, v in cluster_aliases.items()
            ],
            'function kns {{ c=$(kuba ns --command-multi "$@") && [[ -n "$c" ]] && _kuba_history_evl "$c" || return 0 ; }}',
            'function kctx {{ c=$(kuba ctx --command-multi "$@") && [[ -n "$c" ]] && _kuba_history_evl "$c" || return 0 ; }}',
            'function kctxn {{ c=$(kuba ctx --ns --command-multi "$@") && [[ -n "$c" ]] && _kuba_history_evl "$c" || return 0 ; }}',
            'function kclus {{ c=$(kuba cluster --command-multi "$@") && [[ -n "$c" ]] && _kuba_history_evl "$c" || return 0 ; }}',
            'function kssh {{ c=$(kuba ssh --command --loud {bastion} {name} "$@") && [[ -n "$c" ]] && _kuba_history_evl "$c" ; }}',
            'function kssha {{ c=$(kuba ssh --command --loud {bastion} {name} --any "$@") && [[ -n "$c" ]] && _kuba_history_evl "$c" ; }}',
            'function ksshp {{ c=$(kuba ssh --command --loud {bastion} {name} --pod "$@") && [[ -n "$c" ]] && _kuba_history_evl "$c" ; }}',
        ],
    }[shell]
    return [
        tpl.format(
            kubectl=kubectl,
            bastion=f"'--bastion={ssh_bastion}'" if ssh_bastion else "",
            name="--use-name" if ssh_use_name else "",
        )
        for tpl in tpls
    ]


def shellenv_kuba(shell: str) -> list[str]:
    lines = {
        "": [],
        "zsh": [
            "",
            "# KUBA",
            "",
            *kuba_completion_lines("zsh"),
            "",
        ],
    }[shell]
    return lines


def kuba_completion_lines(shell: str) -> list[str]:
    completion_cls = click.shell_completion.get_completion_class(shell)
    if not completion_cls:
        raise ColorizedClickException(f"unsupported shell '{shell}'")
    completion = completion_cls(click.Command("kuba", dict()), dict(), "kuba", "_KUBA_COMPLETE")
    return completion.source().splitlines()


def shellenv_resources(rtypes: dict[str, str], shell: str, no_native: bool, debug) -> list[str]:
    lines_common = [
        "",
        "# RESOURCE LEVEL",
        "",
    ]
    tpls_aliases_shared = [
        "alias k{char}{a}{m}{x}{z}='kuba get {leader} {select} {all} {multi} {rtype}'",
        "alias k{char}{a}{m}{x}{z}d='kuba describe {leader} {select} {all} {multi} {rtype}'",
        "alias k{char}{a}{m}{x}{z}n='kuba get {leader} {select} {all} {multi} --output=name {rtype}'",
        "alias k{char}{a}{m}{x}{z}w='kuba get {leader} {select} {all} {multi} --output=wide {rtype}'",
        "alias k{char}{a}{m}{x}{z}y='kuba get {leader} {select} {all} {multi} --output=yaml {rtype}'",
        "alias k{char}{a}{m}{x}{z}j='kuba get {leader} {select} {all} {multi} --output=json {rtype}'",
        "alias k{char}{a}{m}{x}{z}e='kuba get {leader} {select} {all} {multi} --output=events {rtype}'",
        "alias k{char}{a}{m}{x}{z}c='kuba get {leader} {select} {all} {multi} --output=children {rtype}'",
        "alias k{char}{a}{m}{x}{z}u='kuba get {leader} {select} {all} {multi} --output=parents {rtype}'",
        "alias k{char}{a}{m}{x}{z}g='kuba get {leader} {select} {all} {multi} --output=logs {rtype}'",
        "alias k{char}{a}{m}{x}{z}l='kuba get {leader} {select} {all} {multi} --output=logs-follow {rtype}'",
    ]
    tpls_aliases = {
        "": [
            *tpls_aliases_shared,
            'function k{char}{a}{m}{x}{z}f() {{ kuba get {leader} {select} --must-single-command="using fx" {all} {multi} --output=json {rtype} "$@" | kuba ifne fx ; }}',
        ],
        "zsh": [
            *tpls_aliases_shared,
            # Treat fx as a special case because otherwise closing fx => losing all output
            'function k{char}{a}{m}{x}{z}f() {{ c=$(kuba get --command {leader} {select} --must-single-command="using fx" {all} {multi} --output=json {rtype} "$@") && [[ -n "$c" ]] && _kuba_history_evl "$c | kuba ifne fx" ; }}',
        ],
    }[shell]
    tpls_completion = {
        "": [],
        "zsh": [tpl_zsh_alias],
    }[shell]
    rtypes = rtypes if no_native else NATIVE_RTYPES | rtypes  # selectively overrides default native rtypes

    lines = []

    lines.extend(lines_common)

    lines_aliases = []
    for char, rtype in rtypes.items():
        tpls = tpls_aliases[:]
        if rtype == "node":
            tpls.append("alias k{char}{a}{m}{x}{z}o='kuba get {leader} {select} {all} {multi} --output=pods {rtype}'")
        elif rtype == "pod":
            tpls.append("alias k{char}{a}{m}{x}{z}r='kuba get {leader} {select} {all} {multi} --output=containers {rtype}'")
            tpls.append("alias k{char}{a}{m}{x}{z}o='kuba get {leader} {select} {all} {multi} --output=node {rtype}'")
        for tpl in tpls:
            for a, m, x, z in product(["", "a", "k"], ["", "m"], ["", "x"], ["", "z", "p"]):
                all_ = {"": "", "a": "--all-namespaces", "k": "--namespace kube-system"}[a]
                multi = "--multi-cluster" if m else ""
                leader = "--leader" if x else ""
                select = {"": "", "z": "--select", "p": "--no-select"}[z]
                line_alias = tpl.format(char=char, a=a, m=m, x=x, z=z, rtype=rtype, leader=leader, select=select, all=all_, multi=multi)
                line_alias = re.sub(r" {2,}", " ", line_alias)
                lines_aliases.append(line_alias)
    lines.extend(lines_aliases)
    kfunc_names = get_kfunc_names(lines)

    lines_completion = []
    for kfunc in kfunc_names:
        for tpl in tpls_completion:
            lines_completion.append(tpl(kfunc, debug, rtypes=rtypes))
    lines.extend(lines_completion)

    if kfunc_names:
        lines_p9k = [f"POWERLEVEL9K_KUBECONTEXT_SHOW_ON_COMMAND+='|{'|'.join(kfunc_names)}'"]
        lines.extend(lines_p9k)

    return lines


def shellenv_containers(shell: str, debug: bool) -> list[str]:
    lines_common = [
        "",
        "# CONTAINER LEVEL",
        "",
    ]
    tpls_aliases = {
        "": [
            'function k{char}{a}{m}{x}{c}{extra_char}() {{ kuba {subcommand} {container} {leader} {all} {multi} {e_before} "$@" {e_after} ; }}',
        ],
        "zsh": [
            'function k{char}{a}{m}{x}{c}{extra_char}() {{ c=$(kuba {subcommand} --command {container} {leader} {all} {multi} {e_before} "$@" {e_after}) && _kuba_history_evl "$c" ; }}',
        ],
    }[shell]
    tpls_completion = {
        "": [],
        "zsh": [tpl_zsh_alias],
    }[shell]

    subcommands = {
        "l": "logs",
        "e": "exec",
    }
    subcommand_extras = {
        "logs": {
            "": ("", ""),
            "f": ("--follow", ""),
        },
        "exec": {
            "": ("--loud", ""),
        },
    }

    lines = []

    lines.extend(lines_common)

    lines_aliases = []
    for tpl in tpls_aliases:
        for char, subcommand in subcommands.items():
            for extra_char, (e_before, e_after) in subcommand_extras[subcommand].items():
                for a, m, x, c in product(["", "a", "k"], ["", "m"], ["", "x"], ["", "c", "l"]):
                    all_ = {"": "", "a": "--all-namespaces", "k": "--namespace kube-system"}[a]
                    multi = "--multi-cluster" if m else ""
                    leader = "--leader" if x else ""
                    container = {"": "--guess", "c": "", "l": "--all"}[c]
                    line_alias = tpl.format(
                        char=char,
                        a=a,
                        m=m,
                        x=x,
                        c=c,
                        extra_char=extra_char,
                        subcommand=subcommand,
                        leader=leader,
                        container=container,
                        all=all_,
                        multi=multi,
                        e_before=e_before,
                        e_after=e_after,
                    )
                    line_alias = re.sub(r" {2,}", " ", line_alias)
                    lines_aliases.append(line_alias)
    lines.extend(lines_aliases)
    kfunc_names = get_kfunc_names(lines)

    lines_completion = []
    for kfunc in kfunc_names:
        for tpl in tpls_completion:
            lines_completion.append(tpl(kfunc, debug))
    lines.extend(lines_completion)

    if kfunc_names:
        lines_p9k = [f"POWERLEVEL9K_KUBECONTEXT_SHOW_ON_COMMAND+='|{'|'.join(kfunc_names)}'"]
        lines.extend(lines_p9k)

    return lines


def get_kfunc_names(lines: list[str]) -> list[str]:
    pat = re.compile(r"function (k[a-z]*)")
    return [match.group(1) for line in lines if (match := pat.search(line))]


@cli.command(name="ifne", hidden=True, context_settings=dict(ignore_unknown_options=True, allow_extra_args=True))
@click.pass_context
def ifne_cmd(ctx: click.Context):
    """
    Shadows ifne to run a command only if stdin is non-empty.

    COMMAND is the command to run if stdin is non-empty.

    Usage: kuba ifne COMMAND [ARGS...]
    """
    cmd = ctx.args.copy()

    stdin = get_stdin()
    if not stdin:
        return

    try:
        with subprocess.Popen(cmd, stdin=subprocess.PIPE) as process:
            for chunk in stdin:
                process.stdin.write(chunk)
            process.stdin.close()
            process.wait()
            sys.exit(convert_returncode(process.returncode))
    except BrokenPipeError:
        return  # e.g. if command is head
    except FileNotFoundError:
        raise ColorizedClickException(f"command '{cmd[0]}' not found")
    except PermissionError:
        raise ColorizedClickException(f"permission denied running command '{cmd}'")
    except OSError as e:
        raise ColorizedClickException(f"failed running command '{cmd}': {e}")


def get_stdin() -> Optional[Iterator[bytes]]:
    """Get stdin as an iterator of bytes, or None if stdin is empty."""

    def lazy(b: bytes, bb: IO[bytes]) -> Iterator[bytes]:
        yield b
        yield from bb

    first_byte = sys.stdin.buffer.read(1)
    if not first_byte:
        return None
    return lazy(first_byte, sys.stdin.buffer)


def convert_returncode(sig: int) -> int:
    return sig if sig >= 0 else 128 + (-sig)  # negative=>system-originated => add 128 to indicate signal exit


@cli.command(name="ns")
@click.argument("nquery", metavar="NAMESPACE", required=False, default="", shell_complete=complete_namespace)
@click.option("--list", "just_list", is_flag=True, help="Just list available namespaces.")
@click.option("--kubectl", default=os.getenv("KUBA_KUBECTL", "kubectl"), help="Name or path of the kubectl binary to use.")
@click.option("--command-multi", "try_print", is_flag=True, help="If more than one result, just print the final command that would have been run.")
@click.option("--debug", is_flag=True, default=DEBUG, help="Print debug info to stderr.")
def ns_cmd(nquery: str, just_list: bool, kubectl: str, try_print: bool, debug: bool):
    """
    Enhances kubens.

    NAMESPACE is a namespace name or query.

    Usage: kuba ns [NAMESPACE]
    """
    _ns_cmd(nquery, just_list, kubectl, try_print, debug)


def _ns_cmd(nquery: str, just_list: bool, kubectl: str, try_print: bool, debug: bool):
    namespaces = get_namespaces(kubectl, "", nquery, debug)
    if just_list:
        print_if_isatty(namespaces.header, kubectl)
        print_color("\n".join(namespaces.descriptions()), kubectl)
        return

    namespace = choose_resource("namespace", nquery, namespaces)
    just_print = try_print and len(namespaces) > 1
    if just_print:
        cmd = Command(["kns", namespace.name], "", "")
    else:
        cmd = Command([kubectl, "config", "set-context", "--current", "--namespace", namespace.name], "", "")
        log_color(f'{GREEN}Active namespace is "{namespace.name}".{RESET}', kubectl, True)
    log(f"cmd: {cmd}", debug)
    cmd.run(just_print, kubectl, stderr_only=True)


def get_namespaces(kubectl: str, context: str, nquery: str, debug: bool) -> Resources:
    return get_resources(kubectl, context, "namespace", nquery, "", False, [], "", "", debug)


def get_namespace(kubectl: str, debug: bool) -> str:
    cmd = [kubectl, "config", "view", "--minify", "--output=jsonpath={..namespace}"]
    log(f"get_namespace: {cmd=}", debug)
    try:
        return subprocess.check_output(cmd, text=True).strip()
    except subprocess.CalledProcessError as e:
        raise ColorizedClickException(f"error fetching current namespace: {e}")


@cli.command(name="ctx")
@click.argument("xquery", metavar="CONTEXT", required=False, default="", shell_complete=complete_context)
@click.option("--list", "just_list", is_flag=True, help="Just list available contexts.")
@click.option("--nss", "keep_ns", is_flag=True, help="Try to keep the current namespace.")
@click.option("--ns", "force_keep_ns", is_flag=True, help="Try to keep the current namespace, selecting if not possible.")
@click.option(
    "--clusters", "cluster_groups", default=os.getenv("KUBA_CLUSTERS", ""), callback=parse_cluster_groups, help="Comma-separated list of clusters to consider."
)
@click.option("--kubectl", default=os.getenv("KUBA_KUBECTL", "kubectl"), help="Name or path of the kubectl binary to use.")
@click.option("--command-multi", "try_print", is_flag=True, help="If more than one result, just print the final command that would have been run.")
@click.option("--debug", is_flag=True, default=DEBUG, help="Print debug info to stderr.")
def ctx_cmd(xquery: str, just_list: bool, keep_ns: bool, force_keep_ns: bool, cluster_groups: ClusterGroups, kubectl: str, try_print: bool, debug: bool):
    """
    Enhances kubectx.

    CONTEXT is a context name or query.

    Usage: kuba ctx [CONTEXT]
    """
    _ctx_cmd(xquery, just_list, keep_ns, force_keep_ns, cluster_groups, kubectl, try_print, debug)


def _ctx_cmd(xquery: str, just_list: bool, keep_ns: bool, force_keep_ns: bool, cluster_groups: ClusterGroups, kubectl: str, try_print: bool, debug: bool):
    contexts = get_contexts(kubectl, xquery, debug)
    if just_list:
        print_if_isatty(contexts.header, kubectl)
        print_color("\n".join(contexts.descriptions()), kubectl)
        return
    context = choose_resource("context", xquery, contexts)
    just_print = try_print and len(contexts) > 1
    if just_print:
        cmd = Command(
            remove_empty(
                [
                    "kctx",
                    "--ns" if keep_ns else "",
                    context.name,
                ]
            ),
            "",
            "",
        )
    else:
        cmd = Command([kubectl, "config", "use-context", context.name], "", "")

    is_sibling = context.name in cluster_groups.get(get_context(kubectl, debug), ())
    start_ns = get_namespace(kubectl, debug)
    target_ns = start_ns if force_keep_ns or (keep_ns and is_sibling) else None

    log(f"cmd: {cmd}", debug)
    cmd.run(just_print, kubectl, stderr_only=True)

    if just_print or not (keep_ns or force_keep_ns):
        return

    end_ns = get_namespace(kubectl, debug)
    if end_ns == target_ns or not target_ns:
        log_color(f'{GREEN}Active namespace is "{end_ns}".{RESET}', kubectl, True)
        return

    if not get_namespaces(kubectl, "", "", debug).filter_by_name(target_ns):
        target_ns = choose_resource("namespace", "", get_namespaces(kubectl, "", "", debug)).name

    cmd = Command([kubectl, "config", "set-context", "--current", "--namespace", target_ns], "", "")
    log_color(f'{GREEN}Active namespace is "{target_ns}".{RESET}', kubectl, True)
    log(f"cmd: {cmd}", debug)
    cmd.run(just_print, kubectl, stderr_only=True)


def get_contexts(kubectl: str, xquery: str, debug: bool) -> Resources:
    cmd = remove_empty([kubectl, color(kubectl), "config", "get-contexts"])
    log(f"get_contexts: {cmd=}", debug)
    try:
        stdout_lines = subprocess.check_output(cmd, text=True).strip().splitlines()
    except subprocess.CalledProcessError as e:
        raise ColorizedClickException(f"error fetching resources: {e}")
    return make_resources(kubectl, "", "context", xquery, stdout_lines, "", False, False, debug, indices=(1, -1, -1))  # HACK: contexts aren't really resources


def get_context(kubectl: str, debug: bool) -> str:
    cmd = [kubectl, "config", "current-context"]
    log(f"get_context: {cmd=}", debug)
    try:
        return subprocess.check_output(cmd, text=True).strip()
    except subprocess.CalledProcessError as e:
        raise ColorizedClickException(f"error fetching current context: {e}")


@cli.command(name="cluster")
@click.argument("xquery", metavar="CONTEXT", required=False, default="", shell_complete=complete_context)
@click.argument("nquery", metavar="NAMESPACE", required=False, default="", shell_complete=complete_namespace)
@click.option("--kubectl", default=os.getenv("KUBA_KUBECTL", "kubectl"), help="Name or path of the kubectl binary to use.")
@click.option("--command-multi", "try_print", is_flag=True, help="If more than one result, just print the final command that would have been run.")
@click.option("--debug", is_flag=True, default=DEBUG, help="Print debug info to stderr.")
def cluster_cmd(xquery: str, nquery: str, kubectl: str, try_print: bool, debug: bool):
    """
    Combines kubectx + kubens for all-in-one switching between clusters.

    CONTEXT is a context name or query.
    NAMESPACE is a namespace name or query.

    Usage: kuba cluster [CONTEXT [NAMESPACE]]
    """
    contexts = get_contexts(kubectl, xquery, debug)
    context = choose_resource("context", xquery, contexts)
    nquery = get_namespace(kubectl, debug) if nquery == "-" else nquery
    namespaces = get_namespaces(kubectl, context.name, nquery, debug)
    if nquery in namespaces:
        namespaces = namespaces.filter_by_name(nquery)
        namespace = namespaces.pop()
    else:
        namespace = choose_resource("namespace", nquery, namespaces)

    just_print = try_print and (len(contexts) > 1 or len(namespaces) > 1)
    if just_print:
        cmds = Commands(
            [
                Command(["kclus", context.name, namespace.name], "", ""),
            ]
        )
        log(f"cmds: {cmds}", debug)
        cmds.run("cluster", just_print, kubectl, stderr_only=True)
        return
    _ctx_cmd(
        xquery=context.name,
        just_list=False,
        keep_ns=not nquery,  # skip ns change if namespace is specified
        force_keep_ns=False,
        cluster_groups=ClusterGroups(),
        kubectl=kubectl,
        try_print=try_print,
        debug=debug,
    )
    _ns_cmd(
        nquery=namespace.name,
        just_list=False,
        kubectl=kubectl,
        try_print=try_print,
        debug=debug,
    )


PartialOutputType = click.Choice(
    [
        "name",
        "wide",
    ],
    case_sensitive=False,
)

FullOutputType = click.Choice(
    [
        "describe",
        "name",
        "wide",
        "yaml",
        "json",
        "fx",
        "events",
        "logs",
        "logs-follow",
        "children",
        "parents",
        "pods",  # only for nodes
        "node",  # only for pods
        "containers",  # only for pods
    ],
    case_sensitive=False,
)


@cli.command(name="hostname")
@click.argument("hquery", metavar="QUERY", shell_complete=complete_pod)
@click.option("--kubectl", default=os.getenv("KUBA_KUBECTL", "kubectl"), help="Name or path of the kubectl binary to use.")
@click.option("--debug", is_flag=True, default=DEBUG, help="Print debug info to stderr.")
def hostname_cmd(hquery: str, kubectl: str, debug: bool):
    """
    Convert between node names and hostnames.

    - If passed a node name (can be fuzzy) => print hostname
    - If passed a hostname (must be exact) => print node name

    Usage: kuba hostname QUERY
    """
    log(f"args: {hquery=}", debug)

    log("try as node name", debug)
    try:
        nodes = get_resources(kubectl, "", "node", hquery, "", False, [], "", "", debug, do_warn=False)
        if nodes:
            node = choose_resource("node", hquery, nodes)
            print_hostname_of_node(kubectl, node.name, debug)
            return
    except NoMatchException:
        pass

    log("try as hostname label", debug)
    try:
        nodes = get_resources(kubectl, "", "node", "", "", False, [], f"kubernetes.io/hostname={hquery}", "", debug, do_warn=False)
        if nodes:
            print_color("\n".join([f"{WHITE}{n}{RESET}" for n in nodes.names()]), kubectl)
            return
    except NoMatchException:
        pass

    raise ColorizedClickException(f"no node name or hostname matching '{hquery}'")


def print_hostname_of_node(kubectl: str, node_name: str, debug: bool):
    # HACK: manually run the cmd then print its output to standardize presence of trailing newline
    cmd = [
        kubectl,
        "get",
        "node",
        node_name,
        "--no-headers",
        "--output=jsonpath={.metadata.labels.kubernetes\\.io/hostname}",
    ]
    log(f"print_hostname_of_node: {cmd=}", debug)
    try:
        hostnames = subprocess.check_output(cmd, text=True).strip().splitlines()
        if not hostnames:
            raise ColorizedClickException(f"node '{node_name}' has no hostname label")
        print_color(f"{WHITE}{hostnames[0]}{RESET}", kubectl)
    except subprocess.CalledProcessError as e:
        raise ColorizedClickException(f"error fetching hostname for node '{node_name}': {e}")


@cli.command(name="api", context_settings=dict(ignore_unknown_options=True, allow_extra_args=True))
@click.argument("aquery", metavar="TYPE", required=False, default="", shell_complete=complete_rtype)
@click.option("--select/--no-select", default=None, help="Force fzf selection when multiple resources are output.")
@click.option("--one", is_flag=True, help="Only return up to one resource.")
@click.option("--kubectl", default=os.getenv("KUBA_KUBECTL", "kubectl"), help="Name or path of the kubectl binary to use.")
@click.option("--debug", is_flag=True, default=DEBUG, help="Print debug info to stderr.")
@click.option("--command", "just_print", is_flag=True, help="Just print the final command that would have been run.")
@click.option("--output", "-o", "output_fmt", type=PartialOutputType, help="Output format of the resource.")  # HACK: shadow
@click.pass_context
def api_cmd(ctx: click.Context, aquery: str, select: Optional[bool], one: bool, kubectl: str, debug: bool, just_print: bool, output_fmt: str):
    """
    Enhances kubectl api-resources.

    TYPE is a resource type or query, e.g. pod, node, deployment.

    Usage: kuba api [TYPE] [KUBECTL_API_RESOURCES_FLAGS]
    """
    log(f"options: {ctx.params=}", debug)
    log(f"args: {aquery=}, {ctx.args=}", debug)
    (aquery,), extra_args = split_args([aquery])
    ctx.args = extra_args + ctx.args  # try to keep original order
    output_fmt = output_fmt.lstrip("=") if output_fmt else ""  # HACK: -o=wide results in output="=wide", might be a click bug
    log(f"adjusted args: {aquery=}, {ctx.args=}", debug)

    def rectify_api(row: list[str]):
        if len(row) < 5:
            row.insert(1, "-")

    # API resources don't allow getting specific resources, so we can fake it if --select
    should_select = select or bool(aquery)
    if should_select:
        select = Select.YES if select else Select.ALL
        select = select.with_one(one)
        if just_print:
            raise ColorizedClickException("cannot use --command with --select for API resources")
        matching_apis = get_api_resources(kubectl, aquery, debug)
        apis = choose_resources("API resource", aquery, matching_apis, select)
        apis = apis.justify(debug, rectify=rectify_api)
        if output_fmt == "name":
            print_color("\n".join([f"{WHITE}{n}{RESET}" for n in apis.names()]), kubectl)
        else:
            print_if_isatty(apis.header, kubectl)
            print_color("\n".join(apis.descriptions()), kubectl)
        return

    if one:
        raise ColorizedClickException("cannot use --one without --select for API resources")
    cmd = get_api_resources_command(ctx, kubectl, debug, output_fmt)
    log(f"api_cmd: {cmd=}", debug)
    cmd.run(just_print, f"kubectl api-resources")


def get_api_resources(kubectl: str, aquery: str, debug: bool) -> Resources:
    cmd = remove_empty([kubectl, color(kubectl), "api-resources"])
    log(f"get_api_resources: {cmd=}", debug)
    try:
        stdout_lines = subprocess.check_output(cmd, text=True).strip().splitlines()
    except subprocess.CalledProcessError as e:
        raise ColorizedClickException(f"error fetching API resources: {e}")
    return make_resources(kubectl, "", "API resource", aquery, stdout_lines, "", False, False, debug)  # HACK: API resources aren't really resources


def get_api_resources_command(ctx: click.Context, kubectl: str, debug: bool, output_fmt: str) -> Command:
    cmd = remove_empty(
        [
            kubectl,
            "api-resources",
            f"--output={output_fmt}" if output_fmt else "",
            *ctx.args,
        ]
    )
    log(f"get_api_resources: {cmd=}", debug)
    return Command(cmd, "", "")


@cli.command(name="get", context_settings=dict(ignore_unknown_options=True, allow_extra_args=True))
@click.argument("rtype", metavar="TYPE", required=True, shell_complete=complete_rtype_fullname)
@click.argument("rqueries", metavar="RESOURCE", nargs=-1, required=False, shell_complete=complete_resource)
@click.option("--leader", is_flag=True, help="Only consider resources holding a lease.")
@click.option("--select/--no-select", default=None, help="Force fzf selection when multiple resources are output.")
@click.option("--one", is_flag=True, help="Only return up to one resource.")
@click.option("--kubectl", default=os.getenv("KUBA_KUBECTL", "kubectl"), help="Name or path of the kubectl binary to use.")
@click.option("--multi-cluster", is_flag=True, help="List objects across the current cluster's sibling clusters.")
@click.option(
    "--clusters", "cluster_groups", default=os.getenv("KUBA_CLUSTERS", ""), callback=parse_cluster_groups, help="Comma-separated list of clusters to consider."
)
@click.option("--debug", is_flag=True, default=DEBUG, help="Print debug info to stderr.")
@click.option("--command", "just_print", is_flag=True, help="Just print the final command that would have been run.")
@click.option("--must-single-command", help="Fail with provided reason if multiple commands are required to generate the output.")
@click.option("--namespace", "-n", default="", help="Namespace of the pod.")  # HACK: shadow
@click.option("--all-namespaces", "-A", is_flag=True, help="List objects across all namespaces.")  # HACK: shadow
@click.option("--selector", "-l", "label", help="Label to filter resources by.")  # HACK: shadow
@click.option("--output", "-o", "output_fmt", type=FullOutputType, help="Output format of the resource.")  # HACK: shadow
@click.option("--label-columns", "-L", help="Comma-separated list of labels to display as columns.")  # HACK: shadow
@click.option("--sort-by", help="Specify the field to sort by.")  # HACK: shadow
@click.pass_context
def get_cmd(
    ctx: click.Context,
    rtype: str,
    rqueries: tuple[str, ...],
    leader: bool,
    select: Optional[bool],
    one: bool,
    kubectl: str,
    multi_cluster: bool,
    cluster_groups: ClusterGroups,
    debug: bool,
    just_print: bool,
    must_single_command: str,
    namespace: str,
    all_namespaces: bool,
    label: str,
    output_fmt: str,
    label_columns: str,
    sort_by: str,
):
    """
    Enhances kubectl get.

    TYPE is a resource type, e.g. pod, node, deployment.
    RESOURCE is a resource name or query.
    KUBE_GET_FLAGS are additional flags to pass to kubectl get, which MUST be specified last.

    Usage: kuba get TYPE [RESOURCE...] [KUBECTL_GET_FLAGS]
    """
    _generic_kubectl_action(
        ctx,
        "get",
        rtype,
        rqueries,
        leader,
        select,
        one,
        kubectl,
        multi_cluster,
        cluster_groups,
        debug,
        just_print,
        must_single_command,
        namespace,
        all_namespaces,
        label,
        output_fmt,
        label_columns=label_columns,
        sort_by=sort_by,
    )


@cli.command(name="describe", context_settings=dict(ignore_unknown_options=True, allow_extra_args=True))
@click.argument("rtype", metavar="TYPE", required=True, shell_complete=complete_rtype_fullname)
@click.argument("rqueries", metavar="RESOURCE", nargs=-1, required=False, shell_complete=complete_resource)
@click.option("--leader", is_flag=True, help="Only consider resources holding a lease.")
@click.option("--select/--no-select", default=None, help="Force fzf selection when multiple resources are output.")
@click.option("--one", is_flag=True, help="Only return up to one resource.")
@click.option("--kubectl", default=os.getenv("KUBA_KUBECTL", "kubectl"), help="Name or path of the kubectl binary to use.")
@click.option("--multi-cluster", is_flag=True, help="List objects across the current cluster's sibling clusters.")
@click.option(
    "--clusters", "cluster_groups", default=os.getenv("KUBA_CLUSTERS", ""), callback=parse_cluster_groups, help="Comma-separated list of clusters to consider."
)
@click.option("--debug", is_flag=True, default=DEBUG, help="Print debug info to stderr.")
@click.option("--command", "just_print", is_flag=True, help="Just print the final command that would have been run.")
@click.option("--must-single-command", help="Fail with provided reason if multiple commands are required to generate the output.")
@click.option("--namespace", "-n", default="", help="Namespace of the pod.")  # HACK: shadow
@click.option("--all-namespaces", "-A", is_flag=True, help="List objects across all namespaces.")  # HACK: shadow
@click.option("--selector", "-l", "label", help="Label to filter resources by.")  # HACK: shadow
@click.pass_context
def describe_cmd(
    ctx: click.Context,
    rtype: str,
    rqueries: tuple[str, ...],
    leader: bool,
    select: Optional[bool],
    one: bool,
    kubectl: str,
    multi_cluster: bool,
    cluster_groups: ClusterGroups,
    debug: bool,
    just_print: bool,
    must_single_command: str,
    namespace: str,
    all_namespaces: bool,
    label: str,
):
    """
    Enhances kubectl describe.

    TYPE is a resource type, e.g. pod, node, deployment.
    RESOURCE is a resource name or query.
    KUBE_DESCRIBE_FLAGS are additional flags to pass to kubectl describe, which MUST be specified last.

    Usage: kuba describe TYPE [RESOURCE...] [KUBECTL_DESCRIBE_FLAGS]
    """
    _generic_kubectl_action(
        ctx,
        "describe",
        rtype,
        rqueries,
        leader,
        select,
        one,
        kubectl,
        multi_cluster,
        cluster_groups,
        debug,
        just_print,
        must_single_command,
        namespace,
        all_namespaces,
        label,
        "describe",
    )


def _generic_kubectl_action(
    ctx: click.Context,
    action: str,
    rtype: str,
    rqueries: tuple[str, ...],
    leader: bool,
    select: Optional[bool],
    one: bool,
    kubectl: str,
    multi_cluster: bool,
    cluster_groups: ClusterGroups,
    debug: bool,
    just_print: bool,
    must_single_command: str,
    namespace: str,
    all_namespaces: bool,
    label: str,
    output_fmt: str,
    *,
    label_columns: str = "",
    sort_by: str = "",
):
    log(f"options: {ctx.params=}", debug)
    log(f"args: {rtype=}, {rqueries=}, {ctx.args=}", debug)
    args, extra_args = split_args([rtype, *rqueries], min_actual_args=2)
    rtype, rqueries = args[0], args[1:]
    ctx.args = extra_args + ctx.args  # try to keep original order
    output_fmt = output_fmt.lstrip("=") if output_fmt else ""  # HACK: -o=wide results in output="=wide", might be a click bug
    log(f"adjusted args: {rtype=}, {rqueries=}, {ctx.args=}", debug)

    # UX: allow including resource type prefix, e.g. `Pod/pod-name`
    rqueries = [q.lower().removeprefix(f"{rtype}/") for q in rqueries]

    # UX: allow directly specifying a label query
    if len(rqueries) == 1 and "=" in rqueries[0]:
        if label:
            raise ColorizedClickException("cannot use --label with a single resource query containing '='")
        label = rqueries.pop()
    log(f"adjusted rqueries: {rtype=}, {rqueries=}, {label=}", debug)

    clusters, namespace = get_clusters(kubectl, namespace, multi_cluster, cluster_groups, debug)
    select_fmt = output_fmt if output_fmt == "wide" else ""
    early_print = action == "get" and output_fmt in ("", "wide", "name") and not any((*ctx.args, label_columns, sort_by))
    resources = hydrate_resource_queries(
        rtype,
        rqueries,
        namespace,
        all_namespaces,
        clusters,
        label,
        leader,
        get_select(select, action, output_fmt),
        one,
        select_fmt,
        kubectl,
        debug,
        allow_sentinel=not multi_cluster and not early_print,
        do_warn=len(clusters) > 1,
    )
    if not resources:
        return
    if len(resources) > 1:
        if sort_by:
            raise ColorizedClickException(f"cannot use --sort-by with multiple distinct resources: {rtype} {resources.names()}")

    if early_print:
        if output_fmt == "name":
            print_color("\n".join([f"{WHITE}{n}{RESET}" for n in resources.names()]), kubectl)
        else:
            print_resources(resources.justify(debug), kubectl)
        return

    ctx.args = handle_overrides(ctx.args, rtype, len(resources), debug, output_fmt=output_fmt, label_columns=label_columns, sort_by=sort_by)
    all_namespaces = all_namespaces and resources.sentinel  # now only --all-namespaces when listing all resources
    cmds = get_kubectl_generic_action_commands(
        ctx,
        kubectl,
        action,
        rtype,
        resources,
        namespace,
        all_namespaces,
        label,
        output_fmt,
        just_print,
        debug,
    )
    log(f"cmds: {len(cmds)} total: {cmds}", debug)

    cmds.run(f"kubectl {action}", just_print, kubectl, must_single_command=must_single_command)


def get_clusters(kubectl: str, namespace: str, multi_cluster: bool, cluster_groups: ClusterGroups, debug: bool) -> tuple[list[str], str]:
    """Get list of sibling clusters, and the namespace to use for them."""
    if not multi_cluster:
        return [], namespace

    context = get_context(kubectl, debug)
    clusters = list(cluster_groups.get(context, []))
    if not clusters:
        raise ColorizedClickException(f"no sibling clusters found for current context '{context}'")
    namespace = namespace or get_namespace(kubectl, debug)
    if not namespace:
        raise ColorizedClickException(f"no namespace found for current context '{context}'")
    return clusters, namespace


def get_select(select: Optional[bool], action: str, output_fmt: str) -> Select:
    if select is True:
        return Select.YES
    if select is False:
        return Select.ALL

    if action == "describe":
        return Select.YES

    if output_fmt in ("", "wide", "name"):
        return Select.ALL
    if output_fmt in ("pods",):
        return Select.ONE
    if output_fmt in ("parents", "children"):
        return Select.YES

    return Select.YES


def print_resources(resources: Resources, kubectl: str):
    if not resources:
        return
    if resources.header:
        print_if_isatty(resources.header, kubectl, flush=True)
    print_color("\n".join(resources.descriptions()), kubectl, flush=True)


@cli.command(name="logs", context_settings=dict(ignore_unknown_options=True, allow_extra_args=True))
@click.argument("pquery", metavar="POD", required=False, default="", shell_complete=complete_pod)
@click.argument("cquery", metavar="CONTAINER", required=False, default="", shell_complete=complete_container)
@click.option("--leader", is_flag=True, help="Only consider pods holding a lease.")
@click.option("--guess", is_flag=True, help="Heuristically guess a pod's main container and automatically choose it.")
@click.option("--all", "all_containers", is_flag=True, help="Show logs for all containers in the pod.")
@click.option("--kubectl", default=os.getenv("KUBA_KUBECTL", "kubectl"), help="Name or path of the kubectl binary to use.")
@click.option("--multi-cluster", is_flag=True, help="List objects across the current cluster's sibling clusters.")
@click.option(
    "--clusters", "cluster_groups", default=os.getenv("KUBA_CLUSTERS", ""), callback=parse_cluster_groups, help="Comma-separated list of clusters to consider."
)
@click.option("--debug", is_flag=True, default=DEBUG, help="Print debug info to stderr.")
@click.option("--command", "just_print", is_flag=True, help="Just print the final command that would have been run.")
@click.option("--namespace", "-n", default="", help="Namespace of the pod.")  # HACK: shadow
@click.option("--all-namespaces", "-A", is_flag=True, help="List objects across all namespaces.")  # HACK: shadow
@click.option("--selector", "-l", "label", help="Label to filter resources by.")  # HACK: shadow
@click.option("--since", default="", help="Show logs since a specific time, e.g. 1h, 30m, 2d.")  # HACK: shadow
@click.option("--follow", "-f", is_flag=True, help="Specify if the logs should be streamed.")  # HACK: shadow
@click.pass_context
def logs_cmd(
    ctx: click.Context,
    pquery: str,
    cquery: str,
    leader: bool,
    guess: bool,
    all_containers: bool,
    kubectl: str,
    multi_cluster: bool,
    cluster_groups: ClusterGroups,
    debug: bool,
    just_print: bool,
    namespace: str,
    all_namespaces: bool,
    label: str,
    since: str,
    follow: bool,
):
    """
    Enhances kubectl logs.

    POD is a pod name or query.
    CONTAINER is a container name or query.
    KUBECTL_LOG_FLAGS are additional flags to pass to kubectl logs, which MUST be specified last.

    Usage: kuba logs [POD [CONTAINER]] [KUBECTL_LOG_FLAGS]
    """
    log(f"options: {ctx.params=}", debug)
    log(f"args: {pquery=}, {cquery=}, {ctx.args=}", debug)
    (pquery, cquery), extra_args = split_args([pquery, cquery])
    ctx.args = extra_args + ctx.args  # try to keep original order
    log(f"adjusted args: {pquery=}, {cquery=}, {ctx.args=}", debug)

    if not since:
        since = "1s" if follow else "1h"

    if guess and all_containers:
        raise ColorizedClickException("cannot use --guess with --all")

    clusters, namespace = get_clusters(kubectl, namespace, multi_cluster, cluster_groups, debug)
    pods, containers = hydrate_multi_pod_and_multi_container_queries(
        kubectl, pquery, cquery, namespace, all_namespaces, clusters, label, "", leader, guess, all_containers, debug
    )
    cmd = get_kubectl_logs_command(ctx, kubectl, pods, containers, debug, since, follow, just_print)
    log(f"cmd: {cmd}", debug)
    cmd.run(just_print, kubectl)


@cli.command(name="exec", context_settings=dict(ignore_unknown_options=True, allow_extra_args=True))
@click.argument("pquery", metavar="POD", required=False, default="", shell_complete=complete_pod)
@click.argument("cquery", metavar="CONTAINER", required=False, default="", shell_complete=complete_container)
@click.option("--leader", is_flag=True, help="Only consider pods holding a lease.")
@click.option("--guess", is_flag=True, help="Heuristically guess a pod's main container and automatically choose it.")
@click.option("--kubectl", default=os.getenv("KUBA_KUBECTL", "kubectl"), help="Name or path of the kubectl binary to use.")
@click.option("--multi-cluster", is_flag=True, help="List objects across the current cluster's sibling clusters.")
@click.option(
    "--clusters", "cluster_groups", default=os.getenv("KUBA_CLUSTERS", ""), callback=parse_cluster_groups, help="Comma-separated list of clusters to consider."
)
@click.option("--debug", is_flag=True, default=DEBUG, help="Print debug info to stderr.")
@click.option("--command", "just_print", is_flag=True, help="Just print the final command that would have been run.")
@click.option("--loud", is_flag=True, help="Log to stderr, even if --command is specified.")
@click.option("--namespace", "-n", default="", help="Namespace of the pod.")  # HACK: shadow
@click.option("--all-namespaces", "-A", is_flag=True, help="List objects across all namespaces.")  # HACK: needed for shell completion + selection
@click.option("--selector", "-l", "label", help="Label to filter resources by.")  # HACK: shadow
@click.pass_context
def exec_cmd(
    ctx: click.Context,
    pquery: str,
    cquery: str,
    leader: bool,
    guess: bool,
    kubectl: str,
    multi_cluster: bool,
    cluster_groups: ClusterGroups,
    debug: bool,
    just_print: bool,
    loud: bool,
    namespace: str,
    all_namespaces: bool,
    label: str,
):
    """
    Enhances kubectl exec.

    POD is a pod name or query.
    CONTAINER is a container name or query.
    KUBECTL_EXEC_FLAGS are additional flags to pass to kubectl exec, which MUST be specified last.

    Usage: kuba logs [POD [CONTAINER]] [KUBECTL_EXEC_FLAGS]
    """
    log(f"options: {ctx.params=}", debug)
    log(f"args: {pquery=}, {cquery=}, {ctx.args=}", debug)
    (pquery, cquery), extra_args = split_args([pquery, cquery])
    ctx.args = extra_args + ctx.args  # try to keep original order
    log(f"adjusted args: {pquery=}, {cquery=}, {ctx.args=}", debug)

    clusters, namespace = get_clusters(kubectl, namespace, multi_cluster, cluster_groups, debug)
    pod, container = hydrate_pod_and_container_queries(kubectl, pquery, cquery, namespace, all_namespaces, clusters, label, "", leader, guess, debug)

    container_shell = get_container_shell(kubectl, pod, container)
    cmd = get_kubectl_exec_command(ctx, kubectl, pod, container, container_shell, debug)
    if loud or not just_print:
        log_color(f"{WHITE}Connecting to a {container_shell} shell in {pod.name}'s {container}{RESET}", kubectl, True)
    log(f"cmd: {cmd}", debug)
    cmd.run(just_print, kubectl)


@cli.command(name="ssh")
@click.argument("query", metavar="NODE", required=False, default="", shell_complete=complete_ssh)
@click.option(
    "--bastion",
    "bastions",
    callback=parse_kv_simple_regex,
    default=os.getenv("KUBA_SSH_BASTION", ""),
    help="Add bastion host requirements by (regex of) context, formatted as e.g. *staging*=bastion-001,*prod*=bastion-002.",
)
@click.option("--use-name", is_flag=True, default=getenv_bool("KUBA_SSH_USE_NAME"), help="Use node name for SSH instead of default address selection.")
@click.option("--kubectl", default=os.getenv("KUBA_KUBECTL", "kubectl"), help="Name or path of the kubectl binary to use.")
@click.option("--any", "pick_any", is_flag=True, help="Automatically select any ready worker node.")
@click.option("--pod", is_flag=True, help="Choose a pod and ssh into its node.")
@click.option("--debug", is_flag=True, default=DEBUG, help="Print debug info to stderr.")
@click.option("--command", "just_print", is_flag=True, help="Just print the final command that would have been run.")
@click.option("--loud", is_flag=True, help="Log to stderr, even if --command is specified.")
@click.option("--selector", "-l", "label", help="Label to filter resources by.")  # HACK: shadow
def ssh_cmd(
    query: str,
    bastions: dict[re.Pattern, str],
    use_name: bool,
    kubectl: str,
    pod: bool,
    pick_any: bool,
    debug: bool,
    just_print: bool,
    loud: bool,
    label: str,
):
    """
    SSH into a node.

    \b
    Tries to get node IP with the following priority:
    - .status.addresses[?(@.type=='ExternalIP')].address
    - .status.addresses[?(@.type=='InternalIP')].address
    - .status.addresses[?(@.type=='Hostname')].address

    Usage: kuba ssh [NODE]
    """
    log(f"args: {query=}", debug)

    select = Select.YES
    select_fmt = ""
    if pick_any and not pod:
        select = Select.ANY
        label = label or "!node-role.kubernetes.io/master"  # only worker nodes
        select_fmt = r"jsonpath={range .items[?(@.status.conditions[-1].type=='Ready')]}{.metadata.name}{'\n'}{end}"  # only ready nodes

    context = get_context(kubectl, debug)
    bastion = get_bastion(bastions, context)
    if pod:
        pods = hydrate_resource_queries("pod", [query], "", False, [], label, False, select, True, select_fmt, kubectl, debug, do_warn=False)
        if not pods:
            raise ColorizedClickException(f"no pods found for query '{query}'" if query else "no pods found")
        node_name = get_pod_node_name(kubectl, pods.pop(), debug)
    else:
        node = hydrate_resource_queries("node", [query], "", False, [], label, False, select, True, select_fmt, kubectl, debug).pop()
        node_name = node.name
    node_addr = get_node_address(kubectl, node_name, use_name, debug)

    if loud or not just_print:
        bastion_msg = f" via {bastion}" if bastion else ""
        log_color(f"{WHITE}Connecting to {node_addr} in {context}{bastion_msg}{RESET}", kubectl, True)
    cmd = get_ssh_command(node_addr, bastion)
    log(f"cmd: {len(cmd)} total: {cmd}", debug)
    cmd.run(just_print, kubectl)


class Reasons:
    """Reasons why pod can't be scheduled on a node."""

    class E(Enum):
        NODE_NAME = "nodeName"
        NODE_SELECTOR = "nodeSelector"
        NODE_AFFINITY = "nodeAffinity"
        POD_AFFINITY = "podAffinity"
        POD_ANTI_AFFINITY = "podAntiAffinity"
        TAINTS = "taints"

    def __init__(self):
        self.reasons: dict[Reasons.E, list[str]] = {
            Reasons.E.NODE_NAME: [],
            Reasons.E.NODE_SELECTOR: [],
            Reasons.E.NODE_AFFINITY: [],
            Reasons.E.POD_AFFINITY: [],
            Reasons.E.POD_ANTI_AFFINITY: [],
            Reasons.E.TAINTS: [],
        }

    def add(self, typ: "Reasons.E", reason: str):
        self.reasons[typ].append(reason)

    def __bool__(self) -> bool:
        return bool(any(self.reasons.values()))

    def __str__(self) -> str:
        return " ".join(r for reasons in self.reasons.values() for r in reasons)


JSONDict = dict[str, Any]


@cli.command(name="sched")
@click.argument("pquery", metavar="POD", required=False, default="", shell_complete=complete_pod)
@click.option(
    "--file",
    "-f",
    "pod_path",
    type=click.Path(exists=True, dir_okay=False, path_type=Path),
    help="Path to a pod YAML file to use instead of querying for POD.",
)
@click.option("--pods", "include_pods", is_flag=True, help="Also consider pod affinity/anti-affinity rules.")
@click.option("--nodename", "include_nodename", is_flag=True, help="Include nodeName in scheduling constraints.")
@click.option("--pass", "filter_fail", is_flag=True, help="Just print the nodes that pass the scheduling constraints.")
@click.option("--fail", "filter_pass", is_flag=True, help="Just print the nodes that fail the scheduling constraints.")
@click.option("--names", is_flag=True, help="Just print the node names (consider using with --pass or --fail).")
@click.option("--leader", is_flag=True, help="Only consider pods holding a lease.")
@click.option("--kubectl", default=os.getenv("KUBA_KUBECTL", "kubectl"), help="Name or path of the kubectl binary to use.")
@click.option("--debug", is_flag=True, default=DEBUG, help="Print debug info to stderr.")
@click.option("--namespace", "-n", default="", help="Namespace of the pod.")  # HACK: shadow
@click.option("--all-namespaces", "-A", is_flag=True, help="List objects across all namespaces.")  # HACK: shadow
@click.option("--selector", "-l", "label", help="Label to filter resources by.")  # HACK: shadow
@click.pass_context
def sched_cmd(
    ctx: click.Context,
    pquery: str,
    pod_path: Path,
    include_pods: bool,
    include_nodename: bool,
    filter_fail: bool,
    filter_pass: bool,
    names: bool,
    leader: bool,
    kubectl: str,
    debug: bool,
    namespace: str,
    all_namespaces: bool,
    label: str,
):
    """
    Predict which nodes a pod can be scheduled on.

    NOTE: this is a simple structural heuristic that checks a pod's constraints against node and pod labels and taints
    to determine whether the pod could conceivably be scheduled to the node. This is not a full scheduler simulation,
    will not be 100% accurate, and is just a best-effort debugging tool.

    Read failure rationales as "This node will continue to fail while ...".

    POD is a pod name or query.

    Usage: kuba sched [POD]
    """
    log(f"options: {ctx.params=}", debug)
    log(f"args: {pquery=}, {ctx.args=}", debug)

    if filter_fail and filter_pass:
        raise ColorizedClickException("cannot use --pass and --fail at the same time")

    if pod_path:
        if not pod_path.exists():
            raise ColorizedClickException(f"file {pod_path.name} does not exist")
        try:
            pod = yaml.load(pod_path.read_text(), Loader=yaml.Loader)
        except yaml.YAMLError as e:
            raise ColorizedClickException(f"error loading pod YAML: {e}")
    else:
        pod = hydrate_resource_queries("pods", [pquery], namespace, all_namespaces, [], label, leader, Select.YES, True, "", kubectl, debug).pop()
        pod_json = get_for_cluster(kubectl, "", "pod", pod.name, namespace, all_namespaces, label, "json", debug, plain=True, exact=True)
        if not pod_json:
            raise ColorizedClickException(f"no pods found for {pquery}")
        try:
            pod = json.loads(strip_ansi(pod_json))
        except json.JSONDecodeError as e:
            raise ColorizedClickException(f"error loading pod JSON: {e}")
    if not isinstance(pod, dict):
        raise ColorizedClickException(f"pod JSON/YAML is not a valid document")
    if not pod.get("kind") == "Pod":
        raise ColorizedClickException(f"pod JSON/YAML is not a Pod")

    pods = None
    namespaces = dict()
    if include_pods:
        pods_json = get_for_cluster(kubectl, "", "pods", "", "", True, "", "json", debug, plain=True, exact=True)
        try:
            pods = json.loads(pods_json)
        except json.JSONDecodeError as e:
            raise ColorizedClickException(f"error loading pods JSON: {e}")
        if not isinstance(pods, dict):
            raise ColorizedClickException("pods JSON does not contain a valid document")
        pods = pods.get("items", [])
        if not isinstance(pods, list):
            raise ColorizedClickException("pods JSON .items does not contain a list of pods")

        namespaces_json = get_for_cluster(kubectl, "", "namespaces", "", "", False, "", "json", debug, plain=True, exact=True)
        try:
            namespaces = json.loads(namespaces_json)
        except json.JSONDecodeError as e:
            raise ColorizedClickException(f"error loading namespaces JSON: {e}")
        if not isinstance(namespaces, dict):
            raise ColorizedClickException("namespaces JSON does not contain a valid document")
        namespaces = namespaces.get("items", [])
        if not isinstance(namespaces, list):
            raise ColorizedClickException("namespaces JSON .items does not contain a list of namespaces")

    nodes_json = get_for_cluster(kubectl, "", "nodes", "", "", False, "", "json", debug, plain=True, exact=True)
    try:
        nodes = json.loads(nodes_json)
    except json.JSONDecodeError as e:
        raise ColorizedClickException(f"error loading nodes JSON: {e}")
    if not isinstance(nodes, dict):
        raise ColorizedClickException("nodes JSON does not contain a valid document")
    nodes = nodes.get("items", [])
    if not isinstance(nodes, list):
        raise ColorizedClickException("nodes JSON .items does not contain a list of nodes")

    msg = evaluate_nodes(pod, nodes, pods, namespaces, filter_fail, filter_pass, names, include_nodename)
    print_color(msg, "")


def evaluate_nodes(
    pod: JSONDict,
    nodes: list[JSONDict],
    pods: Optional[list[JSONDict]],
    namespaces: list[JSONDict],
    filter_fail: bool,
    filter_pass: bool,
    names: bool,
    include_nodename: bool,
) -> str:
    """Evaluate each node against the pod's scheduling constraints."""
    pod_name = pod.get("metadata", {}).get("name")
    pod_ns = pod.get("metadata", {}).get("namespace")
    if not pod_name or not pod_ns:
        raise ColorizedClickException(f"target pod {pod_name or '<unknown>'}/{pod_ns or '<unknown>'} does not have a name or namespace")

    pods_by_node_label = index_pods(pods, nodes)
    namespaces_by_name = {ns.get("metadata", {}).get("name", "<unknown>"): ns for ns in namespaces}

    msgs: list[tuple[str, str, str]] = []
    for idx, node in enumerate(nodes):
        node_name = node.get("metadata", {}).get("name")
        if not node_name:
            warn(f"node {idx} does not have a name; skipping")
            continue
        pods_by_node_label_for_node = filter_indexed_pods(pods_by_node_label, node)
        reasons = evaluate_node(pod, pod_ns, node, node_name, pods_by_node_label_for_node, namespaces_by_name, include_nodename=include_nodename)
        if reasons:
            if not filter_fail:
                msgs.append((f"{BOLD}{RED}FAIL{RESET}", node_name, str(reasons)))
        else:
            if not filter_pass:
                msgs.append((f"{BOLD}{GREEN}PASS{RESET}", node_name, ""))
    return "\n".join(msg[1] for msg in msgs) if names else "\n".join(justify_fields(msgs))


JSONLookup = dict[str, JSONDict]

JSONLabelLookup = dict[str, list[JSONDict]]  # key->pods
JSONLabelIndex = dict[str, dict[str, list[JSONDict]]]  # key->values->pods


def index_pods(pods: list[JSONDict], nodes: list[JSONDict]) -> JSONLabelIndex:
    """Index pods by node labels."""
    if not pods:
        return dict()

    nodes_by_name = index_nodes(nodes)

    by_node_label = defaultdict(dict)
    for p in pods:
        name = p.get("metadata", {}).get("name")
        ns = p.get("metadata", {}).get("namespace")
        if not name or not ns:
            raise ColorizedClickException(f"pod {name or '<unknown>'}/{ns or '<unknown>'} does not have a name or namespace")

        # NOTE: leaving this out for now, as I feel it could lead to confusion
        # if ns == pod_ns and name == pod_name:
        #     continue  # skip the pod we're checking against

        node_name = p.get("spec", {}).get("nodeName")
        if not node_name:
            continue  # not scheduled to a node

        node_labels = nodes_by_name.get(node_name, {}).get("metadata", {}).get("labels", {})
        for k, v in node_labels.items():
            by_node_label[k][v] = by_node_label[k].get(v, [])  # defaultdict doesn't support nested defaultdicts
            by_node_label[k][v].append(p)
    return dict(by_node_label)


def index_nodes(nodes: list[JSONDict]) -> JSONLookup:
    """Index nodes by name."""
    by_name = dict()
    for n in nodes:
        node_name = n.get("metadata", {}).get("name")
        if not node_name:
            warn(f"node {n} does not have a name; skipping")
        if node_name in by_name:
            warn(f"node {node_name} appears multiple times; skipping duplicate")
        by_name[node_name] = n
    return by_name


def filter_indexed_pods(pods_by_node_label: JSONLabelIndex, node: JSONDict) -> JSONLabelLookup:
    """
    Filter indexed pods by node labels.

    I.e. get all pods on nodes with the same labels as this node, indexed by shared label.
    """
    if not pods_by_node_label:
        return dict()
    labels = node.get("metadata", {}).get("labels", {})
    filtered = defaultdict(list)
    for k, v in labels.items():
        filtered[k].extend(pods_by_node_label.get(k, {}).get(v, []))
    return dict(filtered)


def evaluate_node(
    pod: JSONDict,
    pod_ns: str,
    node: JSONDict,
    node_name: str,
    pods_by_node_label_for_node: JSONLabelLookup,
    namespaces_by_name: JSONLookup,
    *,
    include_nodename=False,
) -> Reasons:
    reasons = Reasons()
    pod_spec = pod.get("spec", {})
    labels = node.get("metadata", {}).get("labels", {})
    fields = {"metadata.name": node_name}

    # .nodeName => bound node
    if include_nodename:
        bound_node = pod_spec.get("nodeName")
        if bound_node and bound_node != node_name:
            reasons.add(Reasons.E.NODE_NAME, f"{BOLD}bound{RESET} {ITALIC}{bound_node}{RESET}")

    # .nodeSelector => node labels
    selectors = pod_spec.get("nodeSelector", {})
    node_labels = node.get("metadata", {}).get("labels", {})
    for k, v_want in selectors.items():
        v_got = node_labels.get(k)
        if v_got != v_want:
            reasons.add(Reasons.E.NODE_SELECTOR, f"{BOLD}label{RESET} {ITALIC}{k}{RESET} {v_got}≠{v_want}")

    # .tolerations => node taints
    pod_tolerations = pod_spec.get("tolerations", [])
    node_taints = node.get("spec", {}).get("taints", [])
    if taints := [t for t in node_taints if not tolerates_taint(pod_tolerations, t)]:
        reasons.add(Reasons.E.TAINTS, f"{BOLD}taint{RESET} {stringify_taints(taints)}")

    # .affinity.nodeAffinity => node selector terms
    if terms := pod_spec.get("affinity", {}).get("nodeAffinity", {}).get("requiredDuringSchedulingIgnoredDuringExecution", {}).get("nodeSelectorTerms", []):
        if not any(pass_node_selector_term(t, labels, fields) for t in terms):
            reasons.add(Reasons.E.NODE_AFFINITY, f"{BOLD}node_afnty{RESET} {stringify_node_selector_terms(terms, labels, fields, neg=True)}")

    if pods_by_node_label_for_node:
        # .affinity.podAffinity => pod affinity terms
        if terms := pod_spec.get("affinity", {}).get("podAffinity", {}).get("requiredDuringSchedulingIgnoredDuringExecution", []):
            if failed := [t for t in terms if not pass_pod_affinity_term(t, pod_ns, pods_by_node_label_for_node, namespaces_by_name)]:
                reasons.add(
                    Reasons.E.POD_AFFINITY,
                    f"{BOLD}pod_afnty{RESET} {stringify_pod_affinity_terms(failed, pod_ns, pods_by_node_label_for_node, namespaces_by_name, neg=True)}",
                )
        # .affinity.podAntiAffinity => pods + the anti-affinity terms they violate
        if terms := pod_spec.get("affinity", {}).get("podAntiAffinity", {}).get("requiredDuringSchedulingIgnoredDuringExecution", []):
            if failed := [t for t in terms if fail_pod_anti_affinity_term(t, pod_ns, pods_by_node_label_for_node, namespaces_by_name)]:
                reasons.add(
                    Reasons.E.POD_ANTI_AFFINITY,
                    f"{BOLD}pod_anti_afnty{RESET} {stringify_pod_anti_affinity_terms(failed, pod_ns, pods_by_node_label_for_node, namespaces_by_name)}",
                )

    return reasons


def stringify_taints(taints: list[JSONDict]) -> str:
    return f" {U_OR} ".join(f"{ITALIC}{t.get('key')}{RESET}={t.get('value')}:{t.get('effect')}" for t in taints)


def pass_node_selector_term(term: JSONDict, labels: JSONDict, fields: JSONDict) -> bool:
    match_exprs = term.get("matchExpressions", [])
    match_fields = term.get("matchFields", [])
    return all(matches_expr(e, labels) for e in match_exprs) and all(matches_expr(e, fields) for e in match_fields)


class AfntyMatch(Enum):
    MATCH = "match"
    WRONG_NS = "wrong_ns"
    NO_MATCH = "no_match"


def pass_pod_affinity_term(
    term: JSONDict,
    pod_ns: str,
    pods_by_node_label_for_node: JSONLabelLookup,
    namespaces_by_name: JSONLookup,
) -> bool:
    passes, _, _, _ = pass_pod_affinity_term_get_pods(term, pod_ns, pods_by_node_label_for_node, namespaces_by_name)
    return passes


def pass_pod_affinity_term_get_pods(
    term: JSONDict,
    pod_ns: str,
    pods_by_node_label_for_node: JSONLabelLookup,
    namespaces_by_name: JSONLookup,
) -> tuple[bool, list[str], list[str], list[str]]:
    match, wrong_ns, no_match = _match_statuses_pod_affinity_term(term, pod_ns, pods_by_node_label_for_node, namespaces_by_name)
    passes = bool(match)
    return passes, match, wrong_ns, no_match


def fail_pod_anti_affinity_term(
    term: JSONDict,
    pod_ns: str,
    pods_by_node_label_for_node: JSONLabelLookup,
    namespaces_by_name: JSONLookup,
) -> bool:
    passes, _, _, _ = fail_pod_anti_affinity_term_get_pods(term, pod_ns, pods_by_node_label_for_node, namespaces_by_name)
    return passes


def fail_pod_anti_affinity_term_get_pods(
    term: JSONDict,
    pod_ns: str,
    pods_by_node_label_for_node: JSONLabelLookup,
    namespaces_by_name: JSONLookup,
) -> tuple[bool, list[str], list[str], list[str]]:
    match, wrong_ns, no_match = _match_statuses_pod_affinity_term(term, pod_ns, pods_by_node_label_for_node, namespaces_by_name)
    fails = bool(match)
    return fails, match, wrong_ns, no_match


def _match_statuses_pod_affinity_term(
    term: JSONDict,
    pod_ns: str,
    pods_by_node_label_for_node: JSONLabelLookup,
    namespaces_by_name: JSONLookup,
) -> tuple[list[str], list[str], list[str]]:
    topology_key = term.get("topologyKey", "")
    if not topology_key:
        raise ColorizedClickException(f"pod affinity term {term} does not have a topologyKey")

    pods = pods_by_node_label_for_node.get(topology_key, {})

    match_statuses = [(p.get("metadata", {}).get("name"), _pod_matches_pod_affinity_term(p, term, pod_ns, namespaces_by_name)) for p in pods]
    match = [p for p, status in match_statuses if status == AfntyMatch.MATCH]
    wrong_ns = [p for p, status in match_statuses if status == AfntyMatch.WRONG_NS]
    no_match = [p for p, status in match_statuses if status == AfntyMatch.NO_MATCH]
    return match, wrong_ns, no_match


def _pod_matches_pod_affinity_term(pod: JSONDict, term: JSONDict, pod_ns: str, namespaces_by_name: JSONLookup) -> AfntyMatch:
    name = pod.get("metadata", {}).get("name")
    if not name:
        warn(f"pod {pod} does not have a name; skipping")
        return AfntyMatch.NO_MATCH
    cur_pod_ns = pod.get("metadata", {}).get("namespace")
    if not cur_pod_ns:
        warn(f"pod {name} does not have a namespace; skipping")
        return AfntyMatch.NO_MATCH
    labels = pod.get("metadata", {}).get("labels", {})

    label_selector = term.get("labelSelector", {})
    namespaces = term.get("namespaces", [])  # default is pod's namespace
    namespace_selector = term.get("namespaceSelector", None)  # nil is pod's namespace, emtpy is all namespaces

    # namespaces and namespaceSelector: matches pod namespaces based on name and labels
    # - present namespaceSelector => matches namespaces based on namespaceSelector
    # - empty namespaceSelector => matches all namespaces
    # - nil namespaceSelector
    #   - nil namespaces => matches pod namespace
    #   - present namespaces => match namespaces
    if namespace_selector:  # present
        namespace_selector = cast(JSONDict, namespace_selector)  # appease type checker
        pod_ns_obj = namespaces_by_name.get(cur_pod_ns)
        if not pod_ns_obj:
            warn(f"pod {name} namespace {cur_pod_ns} does not exist; skipping")
            return AfntyMatch.NO_MATCH
        pod_ns_labels = pod_ns_obj.get("metadata", {}).get("labels", {})
        if not matches_label_selector(pod_ns_labels, namespace_selector):
            return AfntyMatch.WRONG_NS
    elif namespace_selector is not None:  # empty
        pass
    else:  # nil
        namespaces = namespaces or [pod_ns]  # default to pod's namespace
        if cur_pod_ns not in namespaces:
            return AfntyMatch.WRONG_NS

    # labelSelector: matches pod labels
    if not matches_label_selector(labels, label_selector):
        return AfntyMatch.NO_MATCH

    return AfntyMatch.MATCH


def matches_label_selector(labels: JSONDict, label_selector: JSONDict) -> bool:
    for k, v in label_selector.get("matchLabels", {}).items():
        if labels.get(k) != v:
            return False
    for expr in label_selector.get("matchExpressions", []):
        if not matches_expr(expr, labels):
            return False
    return True


def matches_expr(expr: JSONDict, labels: JSONDict) -> bool:
    key, operator, values = expr.get("key"), expr.get("operator"), expr.get("values", [])
    value_got = labels.get(key)

    if operator == "In":
        return value_got in values
    elif operator == "NotIn":
        return value_got not in values
    elif operator == "Exists":
        return key in labels
    elif operator == "DoesNotExist":
        return key not in labels
    elif operator == "Gt":
        try:
            return int(value_got) > int(values[0])
        except (ValueError, TypeError, IndexError):
            raise ColorizedClickException(f"invalid value for Gt operator: {value_got}")
    elif operator == "Lt":
        try:
            return int(value_got) < int(values[0])
        except (ValueError, TypeError, IndexError):
            raise ColorizedClickException(f"invalid value for Lt operator: {value_got}")
    else:
        raise ColorizedClickException(f"unknown expression operator: {operator}")


def stringify_node_selector_terms(terms: list[JSONDict], labels: JSONDict, fields: JSONDict, *, neg: bool = False) -> str:
    """Stringify node selector terms that weren't met."""
    # Terms are ANDed, expressions are ORed
    str_terms = []
    for term in terms:
        str_fields = []
        for expr in term.get("matchExpressions", []):
            if not matches_expr(expr, labels):
                str_fields.append(stringify_expr(expr, "matchExpression", neg))
        for expr in term.get("matchFields", []):
            if not matches_expr(expr, fields):
                str_fields.append(stringify_expr(expr, "matchFields", neg))
        if str_fields:
            str_terms.append(f" {U_AND} ".join(str_fields))
    if len(str_terms) > 1:
        str_terms = [f"({term})" for term in str_terms]
    return f" {U_OR} ".join(str_terms)


def stringify_expr(expr: Any, typ: str, neg: bool) -> str:
    key, operator, values = expr.get("key"), expr.get("operator"), expr.get("values", [])
    if operator == "In":
        return f"{ITALIC}{key}{RESET}{op(U_IN, neg)}{','.join(values)}"
    if operator == "NotIn":
        return f"{ITALIC}{key}{RESET}{op(U_NIN, neg)}{','.join(values)}"
    if operator == "Exists":
        return f"{op(U_EX, neg)}{ITALIC}{key}{RESET}"
    if operator == "DoesNotExist":
        return f"{op(U_NEX, neg)}{ITALIC}{key}{RESET}"
    if operator == "Gt":
        return f"{ITALIC}{key}{RESET}{op(U_GT, neg)}{values[0]}"
    if operator == "Lt":
        return f"{ITALIC}{key}{RESET}{op(U_LT, neg)}{values[0]}"

    raise ColorizedClickException(f"unknown {typ} operator: {operator}")


def stringify_pod_affinity_terms(
    terms: list[JSONDict],
    pod_ns: str,
    pods_by_node_label_for_node: JSONLabelLookup,
    namespaces_by_name: JSONLookup,
    *,
    neg: bool = False,
) -> str:
    """Stringify pod affinity terms that weren't met."""
    # Terms are ANDed, expressions are ANDed
    str_terms = []
    for term in terms:
        passes, _, _, no_match = pass_pod_affinity_term_get_pods(term, pod_ns, pods_by_node_label_for_node, namespaces_by_name)
        if passes:
            continue
        tk = term.get("topologyKey")
        if not tk:
            raise ColorizedClickException(f"pod affinity term {term} does not have a topologyKey")

        found_no_matches = not no_match
        str_fields = stringify_pod_affinity_term(term, neg, found_no_matches)

        tk_str = f"[{tk}]"
        empty_ind_str = f"{ITALIC}{U_EMPTY}{RESET}" if found_no_matches else None
        joined_fields_str = f" {op(U_AND, neg)} ".join(str_fields) if str_fields else None
        joined_fields_str = f"{BLACK}{strip_ansi(joined_fields_str)}{RESET}" if found_no_matches else joined_fields_str
        term_str = " ".join(s for s in (tk_str, empty_ind_str, joined_fields_str) if s)

        str_terms.append(term_str)

    if len(str_terms) > 1:
        str_terms = [f"({t})" for t in str_terms]
    return f" {op(U_AND, neg)} ".join(str_terms)


def stringify_pod_affinity_term(term: JSONDict, neg: bool, found_no_matches: bool) -> list[str]:
    if found_no_matches:
        neg = not neg
    str_fields = []
    if sel_str := stringify_label_selector(term.get("labelSelector", {}), neg=neg):
        str_fields.append(sel_str)
    if ns_list_str := term.get("namespaces", []):
        str_fields.append(f"namespaces{op(U_IN, neg)}{','.join(ns_list_str)}")
    if namespace_sel_str := stringify_label_selector(term.get("namespaceSelector", {}), neg=neg):
        str_fields.append(namespace_sel_str)
    return str_fields


def stringify_pod_anti_affinity_terms(
    terms: list[JSONDict],
    pod_ns: str,
    pods_by_node_label_for_node: JSONLabelLookup,
    namespaces_by_name: JSONLookup,
) -> str:
    """Stringify pod anti-affinity terms that weren't met."""
    # Terms are ANDed, expressions are ANDed
    str_terms = []
    matching_pods = set[str]()
    for term in terms:
        fails, match, _, _ = fail_pod_anti_affinity_term_get_pods(term, pod_ns, pods_by_node_label_for_node, namespaces_by_name)
        if not fails:
            continue
        else:
            matching_pods.update(set(match))

        str_fields = []
        if sel_str := stringify_label_selector(term.get("labelSelector", {})):
            str_fields.append(sel_str)
        if ns_list_str := term.get("namespaces", []):
            str_fields.append(f"namespaces{U_IN}{','.join(ns_list_str)}")
        if namespace_sel_str := stringify_label_selector(term.get("namespaceSelector", {})):
            str_fields.append(namespace_sel_str)
        if str_fields:
            topo_key = term.get("topologyKey", "<unknown>")
            joined = f" {U_AND} ".join(str_fields)
            str_terms.append(f"[{topo_key}] {joined}")
    if len(str_terms) > 1:
        str_terms = [f"({t})" for t in str_terms]
    matching_str = ",".join(sorted(matching_pods))
    joined = f" {U_AND} ".join(str_terms)
    return f"{BLACK}{matching_str}{RESET} {joined}"


def stringify_label_selector(label_selector: JSONDict, *, neg: bool = False) -> str:
    strs = []
    # matchLabels
    match_labels = label_selector.get("matchLabels", {})
    for key, val in sorted(match_labels.items()):
        strs.append(f"{ITALIC}{key}{RESET}{op(U_EQ, neg)}{val}")
    # matchExpressions
    for expr in label_selector.get("matchExpressions", []):
        strs.append(stringify_expr(expr, "matchExpressions", neg))
    return f" {op(U_AND, neg)} ".join(strs)


def tolerates_taint(tolerations: list[JSONDict], taint: JSONDict) -> bool:
    key = taint.get("key")
    value_taint = taint.get("value")
    effect_taint = taint.get("effect")

    if effect_taint not in ("NoSchedule", "NoExecute"):  # only consider scheduling-related taints
        return True

    for tol in tolerations:
        if tol.get("key") != key:
            continue
        operator = tol.get("operator", "Equal")
        effect_tol = tol.get("effect")
        if effect_tol and effect_tol != effect_taint:
            continue
        if operator == "Exists":
            return True
        elif operator == "Equal":
            if tol.get("value") == value_taint:
                return True
        else:
            raise ColorizedClickException(f"unknown toleration operator: {operator}")

    return False


def justify_fields(lines: list[tuple[str, ...]]) -> list[str]:
    """Justify the fields in the lines to be aligned."""
    max_col_lens = [max(ansi_len(field) for field in col) for col in zip(*lines)]
    justified_lines = []
    for line in lines:
        justified = SEP.join(f"{str(field):<{max_col_len}}" for field, max_col_len in zip(line, max_col_lens))
        justified_lines.append(justified)
    return [line.strip() for line in justified_lines]


def get_pod_node_name(kubectl: str, pod: Resource, debug: bool) -> str:
    ns, cluster = pod.namespace, pod.cluster
    cmd = remove_empty(
        [
            kubectl,
            "get",
            "pod",
            pod.name,
            f"--namespace={ns}" if ns else "",
            f"--context={cluster}" if cluster else "",
            "--output=jsonpath={.spec.nodeName}",
        ]
    )
    log(f"get_pod_node: {cmd=}", debug)
    try:
        node_name = subprocess.check_output(cmd, text=True).strip()
    except subprocess.CalledProcessError as e:
        raise ColorizedClickException(f"fetching pod node: {e}")

    if not node_name:
        raise ColorizedClickException(f"no node found for pod {pod.name}")

    return node_name


def get_node_address(kubectl: str, node_name: str, use_name: bool, debug: bool) -> str:
    if use_name:
        return node_name

    cmd = [kubectl, "get", "node", node_name, "--output=json"]
    log(f"get_node_ip: {cmd=}", debug)
    try:
        node_json_str = subprocess.check_output(cmd, text=True).strip()
    except subprocess.CalledProcessError as e:
        raise ColorizedClickException(f"error fetching node: {e}")

    try:
        node_json = json.loads(node_json_str)
    except json.JSONDecodeError as e:
        raise ColorizedClickException(f"error decoding node JSON: {e}")

    node_addrs = {addr["type"]: addr["address"] for addr in node_json.get("status", {}).get("addresses", [])}
    node_addr = get_first(
        [
            node_addrs.get("ExternalIP"),
            node_addrs.get("InternalIP"),
            node_addrs.get("Hostname"),
        ],
        node_name,
    )
    return node_addr


def get_ssh_command(node_addr: str, bastion: str) -> Command:
    if bastion:
        return Command(["ssh", "-J", bastion, node_addr], "", "")
    return Command(["ssh", node_addr], "", "")


def get_bastion(bastions: dict[re.Pattern, str], context: str) -> str:
    return get_matching(bastions, context)


def get_resource_types(kubectl: str, rtype: str, debug: bool) -> list[str]:
    """Get fully-qualified resource types that match the query."""
    cmd = [kubectl, "api-resources", "--no-headers", "--output=name"]
    log(f"get_resource_types: {cmd=}", debug)
    try:
        rtypes = subprocess.check_output(cmd, text=True).strip().splitlines()
    except subprocess.CalledProcessError as e:
        raise ColorizedClickException(f"error fetching resource types: {e}")
    if not rtypes:
        raise ColorizedClickException("no resource types found")
    rtypes = [rtype] if rtype in rtypes else [r for r in rtypes if is_subseq(rtype, r)]
    if not rtypes:
        raise ColorizedClickException(f"no resource types found for query '{rtype}'")
    return rtypes


def hydrate_multi_pod_and_multi_container_queries(
    kubectl: str,
    pquery: str,
    cquery: str,
    namespace: str,
    all_namespaces: bool,
    clusters: list[str],
    label: str,
    select_fmt: str,
    leader: bool,
    guess: bool,
    all_containers: bool,
    debug: bool,
) -> tuple[Resources, list[str]]:
    pods = get_resources(kubectl, "", "pod", pquery, namespace, all_namespaces, clusters, label, select_fmt, debug, leader=leader)
    pods = choose_resources("pod", pquery, pods, Select.YES)
    if len(pods) == 0:
        raise ColorizedClickException(f"no pods found for query '{pquery}' in {namespace} (all namespaces: {all_namespaces})")

    if all_containers or len(pods) > 1:
        multi_containers = []
    else:
        pod = pods[0]
        containers = get_containers(kubectl, pod, cquery, not guess, debug)
        multi_containers = choose_containers(pod, cquery, containers, guess, debug)

    return pods, multi_containers


def hydrate_pod_and_container_queries(
    kubectl: str,
    pquery: str,
    cquery: str,
    namespace: str,
    all_namespaces: bool,
    clusters: list[str],
    label: str,
    select_fmt: str,
    leader: bool,
    guess: bool,
    debug: bool,
) -> tuple[Resource, str]:
    pods = get_resources(kubectl, "", "pod", pquery, namespace, all_namespaces, clusters, label, select_fmt, debug, leader=leader)
    pod = choose_resource("pod", pquery, pods)

    containers = get_containers(kubectl, pod, cquery, not guess, debug)
    container = choose_container(pod, cquery, containers, guess, debug)

    return pod, container


def get_container_shell(kubectl: str, pod: Resource, container: str) -> str:
    return "bash" if check_container_shell(kubectl, pod, container, "bash") else "sh"  # could check for sh too but prefer quicker return


def check_container_shell(kubectl: str, pod: Resource, container: str, shell: str) -> bool:
    ns, cluster = pod.namespace, pod.cluster
    cmd = remove_empty(
        [
            kubectl,
            f"--namespace={ns}" if ns else "",
            f"--context={cluster}" if cluster else "",
            "exec",
            pod.name,
            f"--container={container}",
            "--",
            "which",
            shell,
        ]
    )
    stdout = subprocess.run(cmd, text=True, capture_output=True).stdout.strip()
    return f"/{shell}" in stdout


def get_resources(
    kubectl: str,
    context: str,
    rtype: str,
    rquery: str,
    namespace: str,
    all_namespaces: bool,
    clusters: list[str],
    label: str,
    select_fmt: str,
    debug: bool,
    *,
    leader: bool = False,
    do_warn: bool = True,
) -> Resources:
    clusters = clusters or [context or ""]
    cluster_to_resources = {
        c: get_resources_for_cluster(kubectl, c, rtype, rquery, namespace, all_namespaces, False, label, select_fmt, debug, leader=leader, do_warn=do_warn)
        for c in clusters
    }
    return Resources.from_clusters(cluster_to_resources, kubectl, debug)


def get_resources_for_cluster(
    kubectl: str,
    context: str,
    rtype: str,
    rquery: str,
    namespace: str,
    all_namespaces: bool,
    multi_cluster: bool,
    label: str,
    select_fmt: str,
    debug: bool,
    *,
    leader: bool = False,
    do_warn: bool = True,
) -> Resources:
    stdout_lines = get_for_cluster(kubectl, context, rtype, rquery, namespace, all_namespaces, label, select_fmt, debug).strip().splitlines()
    return make_resources(
        kubectl,
        context,
        rtype,
        rquery,
        stdout_lines,
        namespace,
        all_namespaces,
        multi_cluster,
        debug,
        header=not select_fmt or select_fmt == "wide",
        leader=leader,
        do_warn=do_warn,
    )


def get_for_cluster(
    kubectl: str,
    context: str,
    rtype: str,
    rquery: str,
    namespace: str,
    all_namespaces: bool,
    label: str,
    select_fmt: str,
    debug: bool,
    *,
    plain: bool = False,
    exact: bool = False,
) -> str:

    def mk_cmd(_rquery: str):
        return remove_empty(
            [
                kubectl,
                color(kubectl, plain=plain),
                f"--context={context}" if context else "",
                "get",
                rtype,
                _rquery or "",
                f"--namespace={namespace}" if namespace else "",
                "--all-namespaces" if all_namespaces else "",
                f"--selector={label}" if label else "",
                f"--output={select_fmt}" if select_fmt else "",
                *handle_overrides([], rtype, 0, debug, output_fmt=select_fmt),
            ]
        )

    cmd = mk_cmd(rquery)  # try first to get the exact request
    log(f"get_for_cluster: {cmd=}", debug)
    try:
        if exact:
            return subprocess.check_output(cmd, text=True)
        else:
            return subprocess.check_output(cmd, text=True, stderr=subprocess.DEVNULL)
    except subprocess.CalledProcessError as e:
        if exact:
            raise ColorizedClickException(f"error fetching resources: {e}")

    cmd = mk_cmd("")  # fall back to getting all resources of the type, then filter later
    log(f"get_for_cluster (fallback): {cmd=}", debug)
    try:
        return subprocess.check_output(cmd, text=True)
    except subprocess.CalledProcessError as e:
        raise ColorizedClickException(f"error fetching resources: {e}")


def make_resources(
    kubectl: str,
    context: str,
    rtype: str,
    rquery: str,
    descriptions: list[str],
    namespace: str,
    all_namespaces: bool,
    multi_cluster: bool,
    debug: bool,
    *,
    header: bool = True,
    leader: bool = False,
    indices: Resource.Indices = None,
    do_warn: bool = True,
) -> Resources:
    """Convert resource descriptions to resources filtered by query."""
    resources = Resources.from_descriptions(descriptions, all_namespaces, multi_cluster, header, indices, namespace=namespace, cluster=context)
    if not resources:
        if rquery:
            if do_warn:
                warn(f"no {rtype} found for query '{rquery}' in {context or '-'}/{namespace or '-'}")
        else:
            if do_warn:
                warn(f"no {rtype} found in {context or '-'}/{namespace or '-'}")
        return Resources([], "", None, cluster=context, namespace=namespace)

    if leader:
        lease_holders = get_lease_holders(kubectl, context, namespace, all_namespaces, debug)
        log(f"get_lease_holders: {lease_holders=}", debug)
        # HACK: it's common to append a random suffix to the lease-requester's name, so this means lease-checking
        # here becomes a heuristic
        # if filtered_to_match := resources.filter(lambda r: (r.namespace, r.name) in lease_holders):
        #     resources = filtered_to_match
        if filtered_to_prefix := resources.filter(lambda r: any([holder[0] == r.namespace and holder[1].startswith(r.name) for holder in lease_holders])):
            resources = filtered_to_prefix
        else:
            resources = Resources([], "", None, cluster=context, namespace=namespace)
    if rquery in resources.names():
        resources = resources.filter_by_name(rquery)
    else:
        resources = resources.filter(lambda r: is_subseq(rquery, r.name))
    if not resources:
        leader_msg = f" leader " if leader else " "
        raise NoMatchException(f"no{leader_msg}{rtype} found for query '{rquery}'")

    if rtype == "node":  # special case for nodes to sort by age
        resources.reverse()

    return resources


def get_containers(kubectl: str, pod: Resource, cquery: str, consider_init: bool, debug: bool) -> list[str]:
    # Handle containers separately since it's not a top-level resource
    ns, cluster = pod.namespace, pod.cluster
    cmd = remove_empty(
        [
            kubectl,
            "get",
            "pod",
            pod.name,
            f"--namespace={ns}" if ns else "",
            f"--context={cluster}" if cluster else "",
            f"--output=jsonpath={{.spec.containers[*].name}}{' {.spec.initContainers[*].name}' if consider_init else ''}",
        ]
    )
    log(f"get_containers: {cmd=}, {consider_init=}", debug)
    try:
        containers = subprocess.check_output(cmd, text=True).strip().split()  # containers aren't newline separated
        containers = [strip_ansi(c) for c in containers]
    except subprocess.CalledProcessError as e:
        raise ColorizedClickException(f"error fetching containers: {e}")
    if not containers:
        raise ColorizedClickException(f"no containers found for pod {pod}")
    containers = [cquery] if cquery in containers else [c for c in containers if is_subseq(cquery, c)]
    if not containers:
        raise ColorizedClickException(f"no containers found for pod {pod} with container query '{cquery}'")
    return containers


def get_lease_holders(kubectl: str, context: str, namespace: str, all_namespaces: bool, debug: bool) -> list[tuple[str, str]]:
    cmd = remove_empty(
        [
            kubectl,
            f"--context={context}" if context else "",
            "get",
            "lease",
            f"--namespace={namespace}" if namespace else "",
            "--all-namespaces" if all_namespaces else "",
            "--no-headers",
            "--output=custom-columns=NAMESPACE:.metadata.namespace,HOLDER:.spec.holderIdentity",
        ]
    )
    log(f"get_lease_holders: {cmd=}", debug)
    try:
        lines = subprocess.check_output(cmd, text=True).strip().splitlines()
        leases = [tuple(line.split()) for line in lines if len(line.split()) == 2]  # needs lease name + holder
        if not all_namespaces:  # handle implicit empty namespace for single-namespace
            leases = [("", holder) for _, holder in leases]
        assert all(len(lease) == 2 for lease in leases), "lease output should be two columns: namespace and holder"
    except subprocess.CalledProcessError as e:
        raise ColorizedClickException(f"error fetching leases: {e}")
    return leases


def hydrate_resource_queries(
    rtype: str,
    rqueries: list[str],
    namespace: str,
    all_namespaces: bool,
    clusters: list[str],
    label: str,
    leader: bool,
    select: Select,
    one: bool,
    select_fmt: str,
    kubectl: str,
    debug: bool,
    *,
    allow_sentinel: bool = False,
    do_warn: bool = True,
) -> Resources:
    """Convert resource queries to resources."""
    rqueries = remove_empty(rqueries)
    select = select.with_one(one).with_n_resources(len(rqueries))
    log(f"hydrate_resource_queries: {rtype=} {rqueries=} {namespace=} {all_namespaces=} {clusters=} {label=} {leader=} {select=}", debug)

    # 0 => get all if possible, else choose
    if not rqueries:
        if allow_sentinel and not select.is_selective() and not leader:
            return all_resources_sentinel(kubectl, namespace, clusters, debug)
        all_resources = get_resources(kubectl, "", rtype, "", namespace, all_namespaces, clusters, label, select_fmt, debug, leader=leader, do_warn=do_warn)
        return choose_resources(rtype, "", all_resources, select)
    # 1 => choose
    elif len(rqueries) == 1:
        rquery = rqueries.pop()
        matching_resources = get_resources(
            kubectl, "", rtype, rquery, namespace, all_namespaces, clusters, label, select_fmt, debug, leader=leader, do_warn=do_warn
        )
        return choose_resources(rtype, rquery, matching_resources, select)
    # 2+ => choose (special handling for getting and filtering)
    else:
        all_resources = get_resources(kubectl, "", rtype, "", namespace, all_namespaces, clusters, label, select_fmt, debug, leader=leader, do_warn=do_warn)
        matching_resources = all_resources.filter(lambda r: any([is_subseq(q, r.name) for q in rqueries]))
        if not matching_resources:
            raise ColorizedClickException(f"no {rtype} found for queries: {', '.join(rqueries)}")
        if not select.is_selective() and len(matching_resources) == len(rqueries):
            return matching_resources
        return choose_resources(rtype, "", matching_resources, select)


def all_resources_sentinel(kubectl: str, namespace: str, clusters: list[str], debug: bool) -> Resources:
    """Empty sentinel Resources, which kubectl will expand to all resources."""
    cluster_to_resources = {c: Resources([Resource("", namespace, c, "")], "", None, sentinel=True) for c in clusters or [""]}
    return Resources.from_clusters(cluster_to_resources, kubectl, debug)


def choose_resources(rtype: str, rquery: str, matching_resources: Resources, select: Select) -> Resources:
    """Choose resources based on query."""
    if not matching_resources:
        return matching_resources

    if not select or select == Select.ALL:
        return matching_resources
    multi = select != Select.ONE

    if rquery in matching_resources.names():
        return matching_resources.filter_by_name(rquery)
    if len(matching_resources) == 1:
        return matching_resources

    if select == Select.ANY:
        return matching_resources.filter_to_one()

    if not multi:
        description = fzf(rquery, matching_resources.descriptions(), rtype, matching_resources.header)
        return matching_resources.filter_by_descriptions([description])
    descriptions = fzf_multi(rquery, matching_resources.descriptions(), rtype, matching_resources.header)
    return matching_resources.filter_by_descriptions(descriptions)


def choose_resource(rtype: str, rquery: str, matching_resources: Resources) -> Resource:
    """Choose resources based on query, ensuring a single resource."""
    return choose_resources(rtype, rquery, matching_resources, Select.ONE).pop()


def choose_containers(pod: Resource, cquery: str, containers: list[str], guess: bool, debug: bool) -> list[str]:
    """Choose containers based on query, ensuring a single container."""
    containers.sort(key=container_scorer(pod, debug), reverse=True)
    if len(containers) == 1 or guess:
        return containers[:1]
    return fzf_multi(cquery, containers, "container", "")


def choose_container(pod: Resource, cquery: str, containers: list[str], guess: bool, debug: bool) -> str:
    """Choose containers based on query, ensuring a single container."""
    containers.sort(key=container_scorer(pod, debug), reverse=True)
    if len(containers) == 1 or guess:
        return containers.pop(0)
    return fzf(cquery, containers, "container", "")


def fzf(query: str, items: list[str], item_name: str, header: str) -> str:
    cmd = remove_empty(
        [
            "fzf",
            "--ansi",
            "--bind=shift-tab:up,tab:down",  # keep tab/shift-tab similar to multi-select
            f"--prompt=Choose {item_name}: ",
            f"--query={query}",
            f"--header={header}" if header else "",
        ]
    )
    return _fzf(cmd, items, item_name).pop()


def fzf_multi(query: str, items: list[str], item_name: str, header: str) -> list[str]:
    item_name = f"{item_name}"
    cmd = remove_empty(
        [
            "fzf",
            "--ansi",
            "--multi",
            f"--prompt=Choose {item_name}: ",
            f"--query={query}",
            f"--header={header}" if header else "",
        ]
    )
    return _fzf(cmd, items, item_name)


def _fzf(cmd: list[str], items: list[str], item_name: str) -> list[str]:
    try:
        lines = subprocess.check_output(cmd, input="\n".join(items), text=True).splitlines()
    except subprocess.CalledProcessError as e:
        if e.returncode == 130:  # fzf exit code for no selection
            raise ColorizedClickException(f"no {item_name} selected")
        raise ColorizedClickException(f"fzf selection failed: {e}")

    # Double-check fzf output
    if not lines:
        raise ColorizedClickException(f"no {item_name} selected")

    return lines


def get_kubectl_generic_action_commands(
    ctx: click.Context,
    kubectl: str,
    action: str,
    rtype: str,
    resources: Resources,
    namespace: str,
    all_namespaces: bool,
    label: str,
    output_fmt: str,
    just_print: bool,
    debug: bool,
) -> Commands:
    cmds = []
    by_cluster_by_namespace = resources.by_cluster_by_namespace()

    n_clusters = len(by_cluster_by_namespace)
    if n_clusters > 1 and output_fmt in ("logs-follow",):
        raise ColorizedClickException(f"can't use {output_fmt} output format with multiple clusters")

    for c, by_namespace in by_cluster_by_namespace.items():
        for n, rs in by_namespace.items():
            cmds.append(
                get_kubectl_generic_action_command(
                    ctx,
                    kubectl,
                    action,
                    rtype,
                    rs,
                    n or namespace,
                    c,
                    all_namespaces,
                    label,
                    output_fmt,
                    just_print,
                    debug,
                )
            )
    return Commands(cmds)


def get_kubectl_generic_action_command(
    ctx: click.Context,
    kubectl: str,
    action: str,
    rtype: str,
    resources: Resources,
    namespace: str,
    cluster: str,
    all_namespaces: bool,
    label: str,
    output_fmt: str,
    just_print: bool,
    debug: bool,
) -> Command:
    if output_fmt in ("parents", "children"):
        if not resources.names():
            raise ColorizedClickException(f"can't specify less than 1 {rtype} for lineage output type")
        if len(resources.names()) > 1:
            raise ColorizedClickException(f"can't specify more than 1 {rtype} for lineage output type")
        cmd = remove_empty(
            [
                kubectl,
                "lineage",
                "--dependencies" if output_fmt == "parents" else "",
                rtype,
                resources.names().pop(),
                f"--namespace={namespace}" if namespace else "",
                f"--context={cluster}" if cluster else "",
                *ctx.args,
            ]
        )
    elif output_fmt in ("logs", "logs-follow"):
        is_multi_pods = rtype.lower() in ("pod", "pods") and len(resources.names()) > 1
        if not resources.names():
            raise ColorizedClickException(f"can't specify less than 1 {rtype} for logs output type")
        if len(resources.names()) > 1 and not is_multi_pods:
            raise ColorizedClickException(f"can't specify more than 1 {rtype} for logs output type")
        has_nofollow = "--no-follow" in ctx.args
        follow = False if has_nofollow else {"logs": False, "logs-follow": True}[output_fmt]
        should_no_follow = not has_nofollow and not follow
        has_since = "--since" in ctx.args
        should_since = follow and not has_since
        rquery = stern_pods_regex(resources, just_print) if is_multi_pods else resources.names().pop()
        cmd = remove_empty(
            [
                kubectl,
                "stern",
                rquery,
                f"--namespace={namespace}" if namespace else "",
                "--all-namespaces" if all_namespaces else "",
                f"--context={cluster}" if cluster else "",
                *ctx.args,
                "--no-follow" if should_no_follow else "",  # stern follows by default
                "--since=1s" if should_since else "",
                "" if sys.stdout.isatty() else "--color=always",
            ]
        )
    elif output_fmt in ("pods",):
        if not resources.names():
            raise ColorizedClickException(f"can't specify less than 1 {rtype} for pods output type")
        if len(resources.names()) > 1:
            raise ColorizedClickException(f"can't specify more than 1 {rtype} for pods output type")
        if rtype not in ("node", "nodes"):
            raise ColorizedClickException(f"pods output type is only supported for node resources, not {rtype}")
        if "--field-selector" in ctx.args:
            raise ColorizedClickException("can't use --field-selector for pods output type")
        cmd = remove_empty(
            [
                kubectl,
                "get",
                "pods",
                f"--field-selector=spec.nodeName={resources.names().pop()}",
                "--all-namespaces",
                f"--context={cluster}" if cluster else "",
                f"--selector={label}" if label else "",
                *ctx.args,
            ]
        )
    elif output_fmt in ("containers",):
        if not resources.names():
            raise ColorizedClickException(f"can't specify less than 1 {rtype} for containers output type")
        if len(resources.names()) > 1:
            raise ColorizedClickException(f"can't specify more than 1 {rtype} for containers output type")
        if rtype not in ("pod", "pods"):
            raise ColorizedClickException(f"containers output type is only supported for pod resources, not {rtype}")
        cmd = remove_empty(
            [
                kubectl,
                "get",
                "pods",
                resources.names().pop(),
                "--output=jsonpath={range .spec.containers[*]}{.name}{'\\n'}{end}",
                "--no-headers",
                f"--namespace={namespace}" if namespace else "",
                f"--context={cluster}" if cluster else "",
                f"--selector={label}" if label else "",
                *ctx.args,
            ]
        )
    elif output_fmt in ("events",):
        if not resources.names():
            raise ColorizedClickException(f"can't specify less than 1 {rtype} for events output type")
        if len(resources.names()) > 1:
            raise ColorizedClickException(f"can't specify more than 1 {rtype} for events output type")
        cmd = remove_empty(
            [
                kubectl,
                "get",
                "events",
                f"--field-selector=involvedObject.kind={rtype_to_kind(rtype, cluster, debug)},involvedObject.name={resources.names().pop()}",
                f"--namespace={namespace}" if namespace else "",
                "--all-namespaces" if all_namespaces else "",
                f"--context={cluster}" if cluster else "",
                "--sort-by=.metadata.creationTimestamp",
                "" if sys.stdout.isatty() else "--no-headers",
                *ctx.args,
            ]
        )
    elif output_fmt in ("describe",):
        if not resources.names():
            raise ColorizedClickException(f"can't specify less than 1 {rtype} for describe output type")
        if len(resources.names()) > 1:
            raise ColorizedClickException(f"can't specify more than 1 {rtype} for describe output type")
        cmd = remove_empty(
            [
                kubectl,
                action,
                rtype,
                resources.names().pop(),
                f"--namespace={namespace}" if namespace else "",
                f"--context={cluster}" if cluster else "",
                *ctx.args,
            ]
        )
    else:
        cmd = remove_empty(
            [
                kubectl,
                action,
                rtype,
                *resources.names(),
                f"--namespace={namespace}" if namespace else "",
                "--all-namespaces" if all_namespaces else "",
                f"--context={cluster}" if cluster else "",
                f"--output={rectify_output_fmt(output_fmt)}" if output_fmt else "",
                "" if sys.stdout.isatty() or action == "describe" else "--no-headers",
                f"--selector={label}" if label else "",
                *ctx.args,
            ]
        )
    return Command(cmd, namespace, cluster)


def rectify_output_fmt(output_fmt: str) -> str:
    """
    Rectify the output format to be compatible with kubectl.

    Kubectl doesn't support bare name output, so we need to use custom columns.
    """
    return "custom-columns=NAME:.metadata.name" if output_fmt == "name" else output_fmt


def get_kubectl_logs_command(
    ctx: click.Context, kubectl: str, pods: Resources, containers: list[str], debug: bool, since: str, follow: bool, just_print: bool
) -> Command:
    namespaces = set(p.namespace for p in pods)
    if len(namespaces) > 1:
        raise ColorizedClickException("cannot log multiple pods in different namespaces at once")
    clusters = set(p.cluster for p in pods)
    if len(clusters) > 1:
        raise ColorizedClickException("cannot log multiple pods in different clusters at once")
    ns, cluster = namespaces.pop(), clusters.pop()

    # Multi-pod
    if len(pods) > 1:
        log_cmd = remove_empty(
            [
                kubectl,
                "stern",
                f"--namespace={ns}" if ns else "",
                f"--context={cluster}" if cluster else "",
                stern_pods_regex(pods, just_print),
                *ctx.args,
                f"--since={since}" if since else "--since=1s",
                "" if follow else "--no-follow",  # stern follows by default
                "" if sys.stdout.isatty() else "--color=always",
            ]
        )
    # Single-pod
    else:
        pod = pods.pop()
        if not check_containers_state(kubectl, pod, containers, debug, allow_terminated=True):
            raise ColorizedClickException(f"container(s) {','.join(containers)} not ready in pod {pod.name}")

        if len(containers) == 1:
            log_cmd = remove_empty(
                [
                    kubectl,
                    "logs",
                    f"--namespace={ns}" if ns else "",
                    f"--context={cluster}" if cluster else "",
                    pod.name,
                    f"--container={containers.pop()}",
                    *ctx.args,
                    f"--since={since}" if since else "--since=1s",
                    "--follow" if follow else "",
                ]
            )
        else:
            log_cmd = remove_empty(
                [
                    kubectl,
                    "stern",
                    f"--namespace={ns}" if ns else "",
                    f"--context={cluster}" if cluster else "",
                    pod.name,
                    f"--container=\"{'|'.join(containers)}\"" if containers else "",
                    *ctx.args,
                    f"--since={since}" if since else "--since=1s",
                    "" if follow else "--no-follow",  # stern follows by default
                ]
            )

    return Command(log_cmd, ns, cluster)


def stern_pods_regex(pods: Resources, just_print: bool) -> str:
    """Stern supports regex for multiple pods: ^(pod1|pod2|pod3)$"""
    r = f'^({"|".join([pod.name for pod in pods])})$'
    if just_print:
        r = f"'{r}'"  # HACK: if just printing for history, need to quote due to special characters
    return r


def get_kubectl_exec_command(ctx: click.Context, kubectl: str, pod: Resource, container: str, container_shell: str, debug: bool) -> Command:
    ns, cluster = pod.namespace, pod.cluster

    if not check_containers_state(kubectl, pod, [container], debug, wanted_states=["running"]):
        raise ColorizedClickException(f"container {container} not ready in pod {pod.name}")

    cmd = remove_empty(
        [
            kubectl,
            f"--namespace={ns}" if ns else "",
            f"--context={cluster}" if cluster else "",
            "exec",
            pod.name,
            "-c",
            container,
            *ctx.args,
            *(["-it", "--", container_shell] if container_shell else ""),
        ]
    )
    return Command(cmd, ns, cluster)


def check_containers_state(
    kubectl: str,
    pod: Resource,
    containers: list[str],
    debug: bool,
    *,
    allow_terminated: bool = False,
    wanted_states: Optional[list] = None,
) -> bool:
    if wanted_states is None:
        wanted_states = ["running", "terminated"]

    ns, cluster = pod.namespace, pod.cluster
    cmd = remove_empty(
        [
            kubectl,
            "get",
            "pod",
            pod.name,
            f"--namespace={ns}" if ns else "",
            f"--context={cluster}" if cluster else "",
            "--output=json",
        ]
    )
    log(f"check_container_ready: {cmd=}, {allow_terminated=}, {wanted_states=}", debug)
    try:
        pod_json = subprocess.check_output(cmd, text=True).strip()
    except subprocess.CalledProcessError as e:
        raise ColorizedClickException(f"check containers ready: fetch pod: {e}")
    try:
        pod_info = json.loads(pod_json)
    except json.JSONDecodeError as e:
        raise ColorizedClickException(f"check containers ready: decode pod JSON: {e}")
    container_statuses = pod_info.get("status", {}).get("containerStatuses", []) + pod_info.get("status", {}).get("initContainerStatuses", [])
    containers_in_loggable_state = {
        c["name"]
        for c in container_statuses
        if any(c.get("state", {}).get(s) for s in wanted_states)  # currently in a wanted state => likely to have logs
        or (allow_terminated and c.get("lastState", {}) is not None)  # has some last state => may have logs
    }
    return all(c in containers_in_loggable_state for c in containers)


def container_scorer(pod: Resource, debug: bool) -> Callable[[str], float]:
    def score_container(container: str) -> float:
        """
        Score a container name on the likelihood it's the pod's main container.

        Scoring:
        - Similarity to pod name
        - Penalize for substrings connoting non-main containers
        """
        substr_to_penalty = {
            "init": 0.1,
            "sidecar": 0.1,
            "adapter": 0.5,
            "exporter": 0.5,
            "proxy": 0.5,
            "agent": 0.7,
            "collector": 0.7,
            "metrics": 0.7,
            "monitor": 0.7,
            "recorder": 0.7,
            "log": 0.9,
        }
        score = SequenceMatcher(None, pod.name, container).ratio()
        for substr, penalty in substr_to_penalty.items():
            if substr in container:
                score *= penalty
        log(f"{score:.5f} {container}", debug)
        return score

    return score_container


def handle_overrides(
    args: list[str], rtype: str, n_resources: int, debug: bool, *, output_fmt: str = "", label_columns: str = "", sort_by: str = ""
) -> list[str]:
    log(f"handle_overrides before: {args=}, {rtype=}, {n_resources=}, {output_fmt=}, {label_columns=}, {sort_by=}", debug)

    if output_fmt != "name":  # guard because name is custom output format => uses custom columns
        if label_columns:
            args = [f"--label-columns={label_columns}"] + args
        elif rtype == "node" and output_fmt not in ("describe", "pods"):
            cols = remove_empty(
                [
                    "node.kubernetes.io/instance-type",
                    "kubernetes.io/hostname" if output_fmt == "wide" else "",
                ]
            )
            args = [f"--label-columns={','.join(cols)}"] + args
        elif rtype == "pod" and output_fmt == "node":
            args = ["--output=custom-columns=NAME:.metadata.name,NODE:.spec.nodeName"] + args

    if sort_by:
        args = [f"--sort-by={sort_by}"] + args
    elif rtype == "node" and output_fmt not in ("describe", "pods") and n_resources <= 1:  # kubectl doesn't allow sorting when specifying multiple resources
        args = ["--sort-by=.metadata.creationTimestamp"] + args

    log(f"handle_overrides after: {args=}, {rtype=}, {n_resources=}, {output_fmt=}, {label_columns=}, {sort_by=}", debug)

    return args


def warn(msg: str):
    """Print a warning message to stderr."""
    click.echo(f"{YELLOW}Warning: {msg}{RESET}", err=True)


def log_color(txt: str, kubectl: str, debug: bool):
    """Log text containing color escape sequences."""
    log(colorize(txt, kubectl=kubectl, out="stderr"), debug)


def print_if_isatty(txt: str, kubectl: str, *, flush: bool = False):
    """Print text if stdout is a tty."""
    if sys.stdout.isatty():
        print_color(txt, kubectl, flush=flush)


def print_color(txt: str, kubectl: str, *, flush: bool = False):
    """Print text containing color escape sequences."""
    print(colorize(txt, kubectl=kubectl, out="stdout"), flush=flush)


def colorize(txt: str, *, kubectl: str = "", out: str = "") -> str:
    """Colorize text containing color escape sequences."""
    if out == "":
        isatty = True
    elif out == "stdout":
        isatty = sys.stdout.isatty()
    elif out == "stderr":
        isatty = sys.stderr.isatty()
    else:
        raise ValueError(f"invalid output stream: {out}")
    if not isatty or not should_color(kubectl):
        return strip_ansi(txt)
    return txt


def color(kubectl: str, *, plain: bool = False) -> str:
    """Force kubecolor to colorize output, if available."""
    if plain:
        return "--plain"
    return "--force-colors" if should_color(kubectl) else ""


def should_color(kubectl: str) -> bool:
    return not kubectl or kubectl.endswith("kubecolor")


class APIResource(NamedTuple):
    singular: str
    plural: str
    kind: str

    @staticmethod
    def from_line(line: str) -> "APIResource":
        """
        Parse a line from `kubectl api-resources` output.

        Line format:
        0. Name (plural, lowercase)
        1. Shortnames [optional]
        2. APIVersion
        3. Namespaced
        4. Kind (singular, capital-cased)
        """
        parts = line.split()
        if len(parts) < 4:
            raise ValueError(f"invalid api-resource line: {line}")
        elif len(parts) == 4:
            parts.insert(1, "-")  # insert placeholder for shortnames
        singular = parts[4].lower()
        plural = parts[0].lower()  # lower just to be extra-safe
        kind = parts[4]
        return APIResource(singular, plural, kind)

    def is_rtype(self, rtype: str) -> bool:
        return rtype.lower() in (self.singular, self.plural)


def rtype_to_kind(rtype: str, context: str, debug: bool) -> str:
    """Canonicalize a resource type to its kind."""
    log(f"rtype_to_kind: {rtype=}", debug)
    rtype = rtype.lower()
    for res in make_api_resources("kubectl", context, debug):
        if res.is_rtype(rtype):
            return res.kind
    raise ColorizedClickException(f"unknown resource type: {rtype}")


@lru_cache(maxsize=1)
def make_api_resources(kubectl: str, context: str, debug: bool) -> list[APIResource]:
    cmd = remove_empty(
        [
            kubectl,
            f"--context={context}" if context else "",
            "api-resources",
            "--no-headers",
        ]
    )
    log(f"make_api_resources: {cmd=}", debug)
    try:
        stdout = subprocess.check_output(cmd, text=True).strip()
        resources = [APIResource.from_line(line) for line in stdout.splitlines() if line]
    except subprocess.CalledProcessError as e:
        raise ColorizedClickException(f"error fetching api-resources: {e}")
    return resources


ANSI_ESCAPE = re.compile(r"\x1B(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~])")


def strip_ansi(txt: str) -> str:
    """
    Strip ANSI escape sequences from text.

    REF: https://stackoverflow.com/questions/14693701
    """
    return ANSI_ESCAPE.sub("", txt)


def ansi_len(txt: str) -> int:
    """Get the length of a string that may contain ANSI escape sequences."""
    return len(strip_ansi(txt))


def ansi_ljust(txt: str, width: int) -> str:
    """Left-justify a string that may contain ANSI escape sequences."""
    len_non_printable = len(txt) - ansi_len(txt)
    return txt.ljust(width + len_non_printable)


def split_args(args: list[str], *, min_actual_args: Optional[int] = None) -> tuple[list[str], list[str]]:
    """
    Stably segregate arguments into args vs. extra_args.

    Click doesn't seem to support non-required arguments and extra args at the same time, so we manually parse the
    non-required arguments and decide if they're actually extra args.
    """
    actual_args, extra_args = split_list(args, lambda x: x.startswith("-"))
    min_actual_args = min_actual_args or len(args)
    actual_args.extend([""] * (min_actual_args - len(actual_args)))  # pad actual_args to original/target length
    return actual_args, extra_args


def split_list(items: list[str], condition: Callable[[str], bool]) -> tuple[list[str], list[str]]:
    """
    Split a list of items into two at the point where the condition is first satisfied.

    The first item that satisfies the condition begins the second list, inclusively.
    """
    idx = next((i for i, item in enumerate(items) if condition(item)), len(items))
    return items[:idx], items[idx:]


@overload
def remove_empty[T](items: list[T]) -> list[T]: ...


@overload
def remove_empty[T](items: set[T]) -> set[T]: ...


def remove_empty(items):
    """Remove empty items from a collection of items."""
    if isinstance(items, list):
        return [item for item in items if item]
    elif isinstance(items, set):
        return {item for item in items if item}
    else:
        raise TypeError("remove_empty only supports lists and sets")


def is_subseq(small: str, big: str):
    """Check if small is a subsequence of big."""
    it = iter(big)
    return all(c in it for c in small)


def get_first[T](it: Iterable[T], default: T) -> T:
    """Get the first truthy item from an iterable, else a default value."""
    return next((x for x in it if x), default)


def get_matching(kv: dict[re.Pattern, str], s: str) -> str:
    """Get the value of the first regex pattern that matches the string."""
    for k, v in kv.items():
        if k.search(s):
            return v
    return ""


if __name__ == "__main__":
    cli()
