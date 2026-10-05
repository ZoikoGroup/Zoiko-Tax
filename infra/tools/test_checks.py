"""Tests for check_tofu.py and check_manifests.py.

The manifest checks are exercised on in-memory documents, so this file needs
no YAML parser and runs on the standard library alone.

    python -m unittest discover -s infra/tools -p "test_*.py"
"""

from __future__ import annotations

import shutil
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import check_manifests as cm  # noqa: E402
import check_tofu as ct  # noqa: E402

DIGEST = "@sha256:" + "a" * 64


def deployment(name="ztax-core", env=None, **pod) -> dict:
    container = {
        "name": name,
        "image": f"ghcr.io/zoikogroup/zoiko-tax/{name}{DIGEST}",
        "securityContext": {
            "readOnlyRootFilesystem": True,
            "allowPrivilegeEscalation": False,
            "capabilities": {"drop": ["ALL"]},
        },
        "env": env if env is not None else [],
    }
    spec = {
        "automountServiceAccountToken": False,
        "enableServiceLinks": False,
        "securityContext": {"runAsNonRoot": True, "seccompProfile": {"type": "RuntimeDefault"}},
        "containers": [container],
    }
    spec.update(pod)
    return {
        "kind": "Deployment",
        "metadata": {"name": name},
        "spec": {"template": {"metadata": {"labels": {cm.GO_RUNTIME_LABEL: "go"}}, "spec": spec}},
    }


def full_env() -> list[dict]:
    _, required, _ = cm.config_load_variables()
    return [{"name": n, "value": "aws-sm://x/y" if n.endswith("_REF") else "v"} for n in sorted(required)]


class GlobIntersection(unittest.TestCase):
    def test_exact(self):
        self.assertTrue(cm.globs_intersect("api.openai.com", "api.openai.com"))
        self.assertFalse(cm.globs_intersect("kms.eu-central-1.amazonaws.com", "api.openai.com"))

    def test_wildcards_on_either_side(self):
        self.assertTrue(cm.globs_intersect("*.amazonaws.com", "bedrock-runtime.*.amazonaws.com"))
        self.assertTrue(cm.globs_intersect("bedrock-runtime.eu-central-1.amazonaws.com", "bedrock-runtime.*.amazonaws.com"))
        self.assertTrue(cm.globs_intersect("*", "openrouter.ai"))

    def test_regional_service_names_do_not_reach_providers(self):
        hosts = cm.provider_hosts()
        for allowed in ["kms.eu-central-1.amazonaws.com", "secretsmanager.eu-central-1.amazonaws.com",
                        "sts.eu-central-1.amazonaws.com", "*.*.svc.cluster.local",
                        "ztax-euc1-dev-01.*.eu-central-1.rds.amazonaws.com"]:
            self.assertFalse([h for h in hosts if cm.globs_intersect(allowed, h)], allowed)


class Configuration(unittest.TestCase):
    def setUp(self):
        self.known, self.required, self.local = cm.config_load_variables()

    def test_config_go_is_parsed(self):
        self.assertIn("ZTAX_DATABASE_URL_REF", self.required)
        self.assertIn("ZTAX_OTLP_ENDPOINT", self.known)
        self.assertNotIn("ZTAX_OTLP_ENDPOINT", self.required)
        self.assertEqual(self.local, "ZTAX_LOCAL_SECRET_")

    def test_migrate_variables_come_from_its_main(self):
        self.assertEqual(cm.getenv_variables("ztax-migrate"), {"ZTAX_MIGRATE_DATABASE_URL", "ZTAX_ENVIRONMENT"})

    def check(self, *docs):
        return cm.check(list(docs))

    def test_a_correct_workload_passes(self):
        self.assertEqual(self.check(deployment(env=full_env())), [])

    def test_unknown_ztax_variable(self):
        problems = self.check(deployment(env=full_env() + [{"name": "ZTAX_DATABSE_URL_REF", "value": "x://y"}]))
        self.assertTrue(any("not a variable ztax-core recognises" in p for p in problems), problems)

    def test_missing_required_variable(self):
        env = [e for e in full_env() if e["name"] != "ZTAX_CELL"]
        self.assertTrue(any("ZTAX_CELL" in p for p in self.check(deployment(env=env))))

    def test_service_links_must_be_off(self):
        problems = self.check(deployment(env=full_env(), enableServiceLinks=True))
        self.assertTrue(any("enableServiceLinks" in p for p in problems))

    def test_local_secrets_are_refused(self):
        env = full_env() + [{"name": "ZTAX_LOCAL_SECRET_POSTGRES", "value": "postgres://u:p@h/db"}]
        self.assertTrue(any("development-only" in p for p in self.check(deployment(env=env))))

    def test_secret_key_ref_outside_the_registered_exception(self):
        env = full_env() + [{"name": "ZTAX_LOG_LEVEL", "valueFrom": {"secretKeyRef": {"name": "s", "key": "k"}}}]
        self.assertTrue(any("materialises a Secret" in p for p in self.check(deployment(env=env))))

    def test_the_registered_exception_passes(self):
        env = [{"name": "ZTAX_ENVIRONMENT", "value": "dev"},
               {"name": "ZTAX_MIGRATE_DATABASE_URL", "valueFrom": {"secretKeyRef": {"name": "s", "key": "dsn"}}}]
        self.assertEqual(self.check(deployment(name="ztax-migrate", env=env)), [])

    def test_development_environment_in_a_cell(self):
        env = [e if e["name"] != "ZTAX_ENVIRONMENT" else {"name": "ZTAX_ENVIRONMENT", "value": "development"} for e in full_env()]
        self.assertTrue(any("never development" in p for p in self.check(deployment(env=env))))

    def test_hardening(self):
        d = deployment(env=full_env())
        d["spec"]["template"]["spec"]["containers"][0]["securityContext"]["readOnlyRootFilesystem"] = False
        d["spec"]["template"]["spec"]["containers"][0]["image"] = "ghcr.io/zoikogroup/zoiko-tax/ztax-core:1.0.0"
        problems = self.check(d)
        self.assertTrue(any("readOnlyRootFilesystem" in p for p in problems))
        self.assertTrue(any("not pinned by digest" in p for p in problems))


class Egress(unittest.TestCase):
    def cnp(self, egress=None, deny=True) -> dict:
        spec = {"endpointSelector": {"matchLabels": {cm.GO_RUNTIME_LABEL: "go"}}, "egress": egress or []}
        if deny:
            spec["egressDeny"] = [{"toCIDRSet": [{"cidr": "0.0.0.0/0", "except": ["10.40.0.0/16"]}]}]
        return {"kind": "CiliumNetworkPolicy", "metadata": {"name": "p"}, "spec": spec}

    def test_fqdn_allowance_reaching_a_provider(self):
        problems = cm.check([deployment(env=full_env()), self.cnp([{"toFQDNs": [{"matchPattern": "*.amazonaws.com"}]}])])
        self.assertTrue(any("can reach model provider" in p for p in problems), problems)

    def test_dns_allowance_reaching_a_provider(self):
        rule = {"toPorts": [{"rules": {"dns": [{"matchName": "api.openai.com"}]}}]}
        self.assertTrue(any("api.openai.com" in p for p in cm.check([deployment(env=full_env()), self.cnp([rule])])))

    def test_world_entity(self):
        problems = cm.check([deployment(env=full_env()), self.cnp([{"toEntities": ["world"]}])])
        self.assertTrue(any("entity" in p for p in problems))

    def test_missing_explicit_deny(self):
        problems = cm.check([deployment(env=full_env()), self.cnp(deny=False)])
        self.assertTrue(any("no explicit egressDeny" in p for p in problems))

    def test_ip_block_in_baseline(self):
        np = {"kind": "NetworkPolicy", "metadata": {"name": "n"},
              "spec": {"podSelector": {}, "egress": [{"to": [{"ipBlock": {"cidr": "0.0.0.0/0"}}]}]}}
        self.assertTrue(any("ipBlock" in p for p in cm.check([np])))


class Tofu(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp(prefix="tofu-test-"))
        self.impl = self.tmp / "aws"
        shutil.copytree(ct.CELL_MODULES / "aws", self.impl, ignore=shutil.ignore_patterns(".terraform", "tests"))
        import json
        self.contract = json.loads((ct.CELL_MODULES / "contract.json").read_text(encoding="utf-8"))
        self.forbidden = json.loads(ct.PROVIDER_ENDPOINTS.read_text(encoding="utf-8"))["aws_vpc_endpoint_services"]

    def tearDown(self):
        shutil.rmtree(self.tmp, ignore_errors=True)

    def append(self, text: str):
        with open(self.impl / "zz_test.tf", "w", encoding="utf-8") as f:
            f.write(text)

    def residency(self):
        return ct.check_residency(self.impl, self.forbidden)

    def test_the_real_module_passes(self):
        self.assertEqual(ct.check_contract(self.impl, self.contract), [])
        self.assertEqual([p for p in self.residency() if "zz_test" not in p], [])

    def test_per_resource_region(self):
        self.append('resource "aws_s3_bucket" "x" {\n  region = var.region\n}\n')
        self.assertTrue(any("per-resource `region`" in p for p in self.residency()))

    def test_region_literal(self):
        self.append('locals {\n  other = "us-east-1"\n}\n')
        self.assertTrue(any("us-east-1" in p for p in self.residency()))

    def test_region_in_a_comment_is_prose(self):
        self.append("# a cell in us-east-1 would be a different cell\n")
        self.assertEqual(self.residency(), [])

    def test_replication(self):
        self.append('resource "aws_s3_bucket_replication_configuration" "x" {}\n')
        self.assertTrue(any("replication" in p for p in self.residency()))

    def test_model_provider_endpoint(self):
        self.append('locals {\n  s = "bedrock-runtime"\n}\n')
        self.assertTrue(any("bedrock-runtime" in p for p in self.residency()))

    def test_contract_drift(self):
        self.append('output "extra" {\n  value = 1\n}\n')
        self.assertTrue(any("outside the contract" in p for p in ct.check_contract(self.impl, self.contract)))


if __name__ == "__main__":
    unittest.main()
