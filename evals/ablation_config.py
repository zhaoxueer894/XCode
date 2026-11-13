"""Resolve the ablation's model settings from the project XCode config."""
from __future__ import annotations

from dataclasses import dataclass, field
import json
from pathlib import Path
import re
from urllib.error import HTTPError, URLError
from urllib.parse import urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener


CONFIG_PATH = Path(__file__).resolve().parents[1] / ".xcode" / "config"
ENV_PREFIX = {"deepseek": "DEEPSEEK", "glm": "GLM", "bailian": "DASHSCOPE"}


@dataclass(frozen=True)
class Model:
    provider: str
    model_id: str
    base_url: str
    host: str
    key: str = field(repr=False)

    def environment(self) -> dict[str, str]:
        prefix = ENV_PREFIX[self.provider]
        return {
            f"{prefix}_API_KEY": self.key,
            f"{prefix}_MODEL": self.model_id,
            f"{prefix}_BASE_URL": self.base_url,
        }


def load_models(path: Path) -> tuple[Model, Model]:
    values = {}
    for number, line in enumerate(path.read_text(encoding="utf-8-sig").splitlines(), 1):
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        name, separator, value = line.partition("=")
        if not separator:
            raise ValueError(f"XCode config line {number} must be key=value")
        values[name.strip()] = value.strip()

    models = []
    for role, supported in (
        ("developer", {"deepseek", "bailian"}),
        ("reviewer", {"glm"}),
    ):
        provider = values.get(role)
        if provider not in supported:
            raise ValueError(f"XCode ablation {role} must be one of {', '.join(sorted(supported))}")
        prefix = f"model.{provider}."
        key, base_url, model_id = (values.get(prefix + name, "") for name in ("key", "base-url", "id"))
        if not all((key, base_url, model_id)):
            raise ValueError(f"XCode config requires {prefix}key, base-url and id")
        endpoint = urlsplit(base_url)
        if (
            endpoint.scheme != "https"
            or not re.fullmatch(r"[a-z0-9][a-z0-9.-]*", endpoint.hostname or "")
            or endpoint.username is not None
            or endpoint.query
            or endpoint.fragment
        ):
            raise ValueError(f"{prefix}base-url must be an HTTPS base URL without credentials, query or fragment")
        models.append(Model(provider, model_id, base_url.rstrip("/"), endpoint.hostname, key))
    return models[0], models[1]


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def probe_model(model: Model) -> None:
    payload = {
        "model": model.model_id,
        "messages": [{"role": "user", "content": "Reply with OK only."}],
        "max_tokens": 16,
    }
    if model.provider == "deepseek":
        payload.update(thinking={"type": "enabled"}, reasoning_effort="high")
    request = Request(
        model.base_url + "/chat/completions",
        data=json.dumps(payload).encode(),
        headers={"Authorization": f"Bearer {model.key}", "Content-Type": "application/json"},
    )
    try:
        with build_opener(NoRedirect()).open(request, timeout=30) as response:
            result = json.load(response)
    except HTTPError as error:
        raise RuntimeError(f"{model.provider} preflight failed (HTTP {error.code})") from None
    except (URLError, OSError, ValueError):
        raise RuntimeError(f"{model.provider} preflight failed (network or invalid response)") from None
    choices = result.get("choices") if isinstance(result, dict) and not result.get("error") else None
    if not isinstance(choices, list) or not any(
        isinstance(choice, dict) and isinstance(choice.get("message"), dict)
        for choice in choices
    ):
        raise RuntimeError(f"{model.provider} preflight did not return a chat completion")
