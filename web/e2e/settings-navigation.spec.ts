import { expect, test } from "@playwright/test";

test("settings navigation reveals and focuses each selected section", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 600 });
  await page.goto("/#/live/settings?section=about");
  const navigation = page.getByRole("navigation", { name: "设置分区" });
  await expect(navigation.getByRole("button")).toHaveCount(4);
  let previous = page.locator("#about-meerkat");
  for (const [label, section, target] of [
    ["常规", "general", "settings-general"],
    ["网络", "network", "settings-network"],
    ["通知", "notifications", "settings-notifications"],
    ["关于 Meerkat", "about", "about-meerkat-title"],
  ]) {
    const button = navigation.getByRole("button", { name: label, exact: true });
    await button.click();
    await expect(page).toHaveURL(new RegExp(`#/live/settings\\?section=${section}$`));
    await expect(button).toHaveAttribute("aria-current", "location");
    await expect(previous).toBeHidden();
    const destination = page.locator(`#${target}`);
    await expect(destination).toBeFocused();
    await expect(destination).toBeInViewport();
    previous = destination;
  }
});

test("reopening update settings twice refocuses About in the current document", async ({ page }) => {
  await page.goto("/#/live/settings?section=about");
  const title = page.locator("#about-meerkat-title");
  await expect(title).toBeFocused();
  const bottom = page.locator("#about-meerkat").locator("[data-scroll-fixture]");
  await page.locator("#about-meerkat").evaluate(section => {
    const spacer = document.createElement("div");
    spacer.style.height = "180vh";
    const marker = document.createElement("div");
    marker.dataset.scrollFixture = "";
    marker.style.height = "1px";
    section.append(spacer, marker);
  });
  for (const nonce of [1, 2]) {
    await bottom.scrollIntoViewIfNeeded();
    await expect(title).not.toBeInViewport();
    await page.evaluate(value => { window.location.hash = `#/live/settings?section=about&open=${value}`; }, nonce);
    await expect(title).toBeFocused();
    await expect(title).toBeInViewport();
    await title.evaluate(heading => heading.blur());
    await expect(title).not.toBeFocused();
  }
});
