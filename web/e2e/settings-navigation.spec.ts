import { expect, test } from "@playwright/test";

test("settings navigation has three working entries that focus and reveal their targets", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 600 });
  await page.goto("/#/live/settings?section=about");
  const navigation = page.getByRole("navigation", { name: "设置分区" });
  await expect(navigation.getByRole("button")).toHaveCount(3);
  // Start with the first target offscreen so focusing without scrolling fails.
  await expect(page.locator("#settings-network")).not.toBeInViewport();
  for (const [label, section, target] of [
    ["网络", "network", "settings-network"],
    ["通知", "notifications", "settings-notifications"],
    ["关于 Meerkat", "about", "about-meerkat-title"],
  ]) {
    const button = navigation.getByRole("button", { name: label, exact: true });
    await button.click();
    await expect(page).toHaveURL(new RegExp(`#/live/settings\\?section=${section}$`));
    await expect(button).toHaveAttribute("aria-current", "location");
    const destination = page.locator(`#${target}`);
    await expect(destination).toBeFocused();
    await expect(destination).toBeInViewport();
  }
  await expect(navigation.locator('[aria-current="location"]')).toHaveCount(1);
});

test("reopening update settings from the tray refocuses the same document", async ({ page }) => {
  await page.goto("/#/live/settings?section=about");
  const title = page.locator("#about-meerkat-title");
  const about = page.locator("#about-meerkat");
  await expect(title).toBeFocused();
  const originalTop = await about.evaluate(section => section.getBoundingClientRect().top);

  // Use another settings section to displace the About section without changing the route.
  await page.locator("#settings-network").evaluate(section => section.scrollIntoView({ block: "start", behavior: "instant" }));
  await expect.poll(() => about.evaluate(section => section.getBoundingClientRect().top)).toBeLessThan(0);
  await title.evaluate(heading => heading.blur());
  await expect(title).not.toBeFocused();

  await page.evaluate(() => {
    window.location.hash = "#/live/settings?section=about&open=" + Date.now();
  });
  await expect(title).toBeFocused();
  await expect.poll(() => about.evaluate(section => section.getBoundingClientRect().top)).toBeCloseTo(originalTop, 0);
});
