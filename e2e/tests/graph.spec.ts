// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import AxeBuilder from "@axe-core/playwright";
import { haveAgent, agentBinary, startAgent } from "./agent";
import { apiCall, apiGet, expect, signIn, test, uniqueKey } from "./fixtures";

test("a run whose jobs need each other shows them as a graph", async ({
  page,
  baseURL,
}) => {
  test.skip(!haveAgent, `${agentBinary} is missing (make build-server)`);
  await signIn(page);
  const key = uniqueKey("G");
  const label = `e2e-${key.toLowerCase()}`;
  await apiCall(page, "POST", "/api/v1/code/projects", {
    key,
    name: `Graph ${key}`,
  });
  await apiCall(page, "POST", `/api/v1/code/projects/${key}/repos`, {
    name: "Shop",
    readme: true,
  });
  const agent = await startAgent(page, baseURL!, label);
  try {
    const job = (name: string, extra = "", run = `echo ${name}`) =>
      `          - name: ${name}\n            runs-on: [shell, ${label}]\n${extra}            steps: [{ run: "${run}" }]\n`;
    // A file of the repository that the pipeline file includes: its variables reach the jobs.
    await apiCall(page, "PUT", `/api/v1/code/repos/${key}/shop/files`, {
      branch: "main",
      path: ".kogge/common.yml",
      content: "variables:\n  GREETING: hello\n",
    });
    await apiCall(page, "PUT", `/api/v1/code/repos/${key}/shop/files`, {
      branch: "main",
      path: ".kogge/pipelines.yml",
      content:
        "version: 1\ninclude: [.kogge/common.yml]\npipelines:\n  ci:\n    name: Graph\n    stages:\n      - name: Build\n        jobs:\n" +
        job("compile", "", "echo $GREETING from compile") +
        "      - name: Test\n        jobs:\n" +
        job("unit", "            needs: [compile]\n") +
        job("lint", "            needs: [compile]\n") +
        job("docs", "            if: changed('docs/**')\n") +
        "      - name: Package\n        jobs:\n" +
        job("package", "            needs: [unit, lint]\n") +
        job("report"),
    });
    await expect
      .poll(
        async () =>
          (await apiGet(page, `/api/v1/code/repos/${key}/shop/runs`)).items[0]
            ?.state ?? "",
        { timeout: 60_000 },
      )
      .toBe("succeeded");

    await page.goto(`/builds/${key.toLowerCase()}/shop/runs/1`);
    const graph = page.getByRole("group", {
      name: "Jobs and what they wait for",
    });
    await expect(graph).toBeVisible();
    // The run names the file it included, at the commit it read it at.
    await expect(
      page
        .getByRole("list", { name: "Included files" })
        .getByRole("link", { name: ".kogge/common.yml" }),
    ).toHaveAttribute(
      "href",
      new RegExp(
        `/code/${key.toLowerCase()}/shop/src/\\.kogge/common\\.yml\\?at=[0-9a-f]{40}$`,
      ),
    );
    // Every job tells what it waits for; lines join the jobs that need each other.
    await expect(
      graph.getByRole("button", { name: "package", exact: true }),
    ).toHaveAccessibleDescription("Stage Package. Waits for unit, lint.");
    await expect(
      graph.getByRole("button", { name: "unit", exact: true }),
    ).toHaveAccessibleDescription("Stage Test. Waits for compile.");
    await expect(
      graph.getByRole("button", { name: "report", exact: true }),
    ).toHaveAccessibleDescription(
      "Stage Package. Waits for the jobs of the earlier stages.",
    );
    await expect(graph.locator(".kogge-graph-edge")).toHaveCount(4);
    await expect(
      graph.locator('.kogge-graph-edge[data-state="succeeded"]'),
    ).toHaveCount(4);
    // From left to right: compile, then unit and lint, then package.
    const x = async (name: string) =>
      (await graph.locator(`[data-job="${name}"]`).boundingBox())!.x;
    expect(await x("compile")).toBeLessThan(await x("unit"));
    expect(await x("unit")).toBe(await x("lint"));
    expect(await x("lint")).toBeLessThan(await x("package"));

    // The selected job's lines stand out; a job its condition skipped says why.
    await graph.getByRole("button", { name: "package", exact: true }).click();
    await expect(
      graph.locator('.kogge-graph-edge[data-active="true"]'),
    ).toHaveCount(2);
    await expect(page.getByRole("heading", { name: "package" })).toBeVisible();
    await expect(
      page.getByText("Waits for unit, lint.", { exact: true }),
    ).toBeVisible();
    await graph.getByRole("button", { name: "compile", exact: true }).click();
    await expect(page.getByRole("log")).toContainText("hello from compile");
    await graph.getByRole("button", { name: "docs", exact: true }).click();
    await expect(
      page.getByText("Skipped: its condition changed('docs/**') is false.", {
        exact: true,
      }),
    ).toBeVisible();

    // Readable for everyone, in both colour schemes.
    for (const scheme of ["light", "dark"] as const) {
      await page.emulateMedia({ colorScheme: scheme });
      await page.waitForLoadState("networkidle");
      const result = await new AxeBuilder({ page })
        .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
        .analyze();
      expect(
        result.violations.map(
          (v) =>
            `${v.id}: ${v.nodes.map((n) => n.target.join(" ")).join(" | ")}`,
        ),
      ).toEqual([]);
    }
  } finally {
    await agent.stop();
  }
});

test("the check of a pipeline file knows needs and conditions", async ({
  page,
}) => {
  await signIn(page);
  const key = uniqueKey("H");
  await apiCall(page, "POST", "/api/v1/code/projects", {
    key,
    name: `Check ${key}`,
  });
  await apiCall(page, "POST", `/api/v1/code/projects/${key}/repos`, {
    name: "Shop",
    readme: true,
  });
  await page.goto(`/code/${key.toLowerCase()}/shop/builds`);
  await page.getByRole("button", { name: "Set up pipelines" }).click();
  await page
    .getByRole("dialog", { name: "Set up pipelines" })
    .getByRole("button", { name: "Minimal" })
    .click();
  const dialog = page.getByRole("dialog", { name: "New file" });
  const content = dialog.getByRole("textbox", { name: "Content" });
  const file = (needs: string, condition: string) =>
    `version: 1\npipelines:\n  ci:\n    stages:\n      - name: Build\n        jobs:\n          - name: build\n            if: ${condition}\n` +
    `            steps: [{ run: make }]\n          - name: test\n            needs: [${needs}]\n            steps: [{ run: make test }]\n`;
  await content.fill(file("biuld", "branch == 'main' && brnch == 'x'"));
  await expect(
    dialog.getByText("The pipeline file has mistakes"),
  ).toBeVisible();
  await expect(
    dialog.getByText('"brnch" is not known', { exact: false }),
  ).toBeVisible();
  await expect(
    dialog.getByText(
      "job test needs biuld, which is not a job of the pipeline",
    ),
  ).toBeVisible();
  await expect(dialog.getByText("line 8, column 37:")).toBeVisible();
  await content.fill(file("build", "branch == 'main'"));
  await expect(dialog.getByText("The pipeline file is valid")).toBeVisible();
  // test needs a job that does not run for every event: worth a second look.
  await expect(dialog.getByText("Worth a second look")).toBeVisible();
  await expect(
    dialog.getByText(
      "job test needs build, which does not run for every event",
      { exact: false },
    ),
  ).toBeVisible();
});
