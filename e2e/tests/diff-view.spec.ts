// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { apiCall, apiGet, expect, signIn, test, uniqueKey } from "./fixtures";

test("review changes word by word, show the lines left out, keep track of viewed files", async ({
  page,
}) => {
  await signIn(page);
  const key = uniqueKey("V");
  const me = await apiGet(page, "/api/v1/me");
  await apiCall(page, "POST", "/api/v1/code/projects", {
    key,
    name: `Viewed ${key}`,
  });
  await apiCall(page, "POST", `/api/v1/code/projects/${key}/repos`, {
    name: "Rates",
    readme: true,
  });
  await apiCall(page, "PUT", `/api/v1/code/projects/${key}/permissions`, [
    {
      holder: { type: "user", id: me.user.id },
      permission: "codeproject.admin",
    },
  ]);
  const repo = `/api/v1/code/repos/${key}/rates`;
  const body = (vat: number, reduced: number) =>
    [
      "package rates",
      ...Array.from({ length: 30 }, (_, i) => `// note ${i + 1}`),
      `const VAT = ${vat} // percent`,
      ...Array.from({ length: 60 }, (_, i) => `// more ${i + 1}`),
      `const Reduced = ${reduced}`,
      ...Array.from({ length: 30 }, (_, i) => `// end ${i + 1}`),
    ].join("\n") + "\n";
  await apiCall(page, "PUT", `${repo}/files`, {
    branch: "main",
    path: "rates.go",
    content: body(16, 5),
  });
  await apiCall(page, "PUT", `${repo}/files`, {
    branch: "main",
    newBranch: "vat",
    path: "rates.go",
    content: body(19, 7),
  });
  const pr = await apiCall(page, "POST", `${repo}/pull-requests`, {
    title: "VAT 19",
    source: "vat",
  });
  await page.goto(
    `/code/${key.toLowerCase()}/rates/pull-requests/${pr.number}`,
  );
  await page.getByRole("tab", { name: "Changes" }).click();
  const table = page.locator("table.kogge-code");
  await expect(table).toBeVisible();

  // Within a changed line, the words that changed stand out.
  await expect(table.locator("tr.del .kogge-word").first()).toHaveText("16");
  await expect(table.locator("tr.add .kogge-word").first()).toHaveText("19");

  // Lines between the hunks are shown in steps, with their numbers on both sides.
  await expect(table.getByText("// more 30", { exact: true })).toHaveCount(0);
  const gap = table.locator("tr.gap").nth(1);
  await gap.getByRole("button", { name: "Show more lines below" }).click();
  const shown = table.locator("tr.expanded", { hasText: "// more 10" });
  await expect(shown).toBeVisible();
  await expect(shown.locator("td.ln").first()).toHaveText("42");
  await expect(table.locator("tr.expanded")).toHaveCount(20);
  await gap
    .getByRole("button", { name: /^Show all \d+ hidden lines$/ })
    .click();
  await expect(table.getByText("// more 30", { exact: true })).toBeVisible();
  await expect(page.locator("tr.gap")).toHaveCount(2);

  // A file marked as viewed folds away, and stays marked after a reload.
  await expect(page.getByText("0 of 1 viewed")).toBeVisible();
  await page.getByRole("checkbox", { name: "Viewed" }).check();
  await expect(table).toHaveCount(0);
  await expect(page.getByText("1 of 1 viewed")).toBeVisible();
  await page.reload();
  await page.getByRole("tab", { name: "Changes" }).click();
  await expect(page.getByRole("checkbox", { name: "Viewed" })).toBeChecked();
  await expect(page.locator("table.kogge-code")).toHaveCount(0);
  await page.getByRole("button", { name: "rates.go" }).click();
  await expect(page.locator("table.kogge-code")).toBeVisible();
});
