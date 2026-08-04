# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

FROM node:22-bookworm

ARG DEBIAN_FRONTEND=noninteractive
ARG INSTALL_CODEX_CLI=false
ARG GO_VERSION=1.25.4

ENV HOME=/home/agent \
    WORKSPACE=/workspace \
    PATH=/usr/local/go/bin:$PATH \
    PYTHONPATH=/usr/local/bin \
    npm_config_update_notifier=false \
    npm_config_fund=false \
    npm_config_audit=false

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        bash \
        ca-certificates \
        coreutils \
        curl \
        dnsutils \
        git \
        jq \
        less \
        make \
        nano \
        openssh-client \
        python3 \
        python3-pip \
        python3-yaml \
        ripgrep \
        tar \
        unzip \
        vim-tiny \
        xz-utils \
    && rm -rf /var/lib/apt/lists/*

RUN go_arch="$(dpkg --print-architecture)" \
    && case "${go_arch}" in \
        amd64) go_arch=amd64 ;; \
        arm64) go_arch=arm64 ;; \
        *) echo "unsupported Go architecture: ${go_arch}" >&2; exit 1 ;; \
    esac \
    && curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${go_arch}.tar.gz" -o /tmp/go.tgz \
    && tar -C /usr/local -xzf /tmp/go.tgz \
    && rm /tmp/go.tgz \
    && go version

RUN if [ "${INSTALL_CODEX_CLI}" = "true" ]; then \
        npm install -g @openai/codex; \
    fi \
    && npm cache clean --force

COPY ai-factory-agent /usr/local/bin/ai-factory-agent
COPY ai-factory-agent.py /usr/local/bin/ai-factory-agent.py
COPY agent_budget.py /usr/local/bin/agent_budget.py
COPY agent_config.py /usr/local/bin/agent_config.py
COPY repair_loop.py /usr/local/bin/repair_loop.py
COPY repair_prompt.py /usr/local/bin/repair_prompt.py
COPY script_validation.py /usr/local/bin/script_validation.py
COPY entrypoint.sh /usr/local/bin/ai-factory-sandbox-entrypoint

RUN sed -i 's/\r$//' /usr/local/bin/ai-factory-agent \
    /usr/local/bin/ai-factory-agent.py \
    /usr/local/bin/agent_budget.py \
    /usr/local/bin/agent_config.py \
    /usr/local/bin/repair_loop.py \
    /usr/local/bin/repair_prompt.py \
    /usr/local/bin/script_validation.py

RUN chmod 755 /usr/local/bin/ai-factory-agent /usr/local/bin/ai-factory-agent.py /usr/local/bin/agent_budget.py /usr/local/bin/agent_config.py /usr/local/bin/repair_loop.py /usr/local/bin/script_validation.py /usr/local/bin/ai-factory-sandbox-entrypoint \
    && useradd --create-home --home-dir "${HOME}" --shell /bin/bash agent \
    && mkdir -p "${WORKSPACE}" \
    && chown -R agent:agent "${HOME}" "${WORKSPACE}"

USER agent
WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/ai-factory-sandbox-entrypoint"]
CMD ["sleep", "3600"]
