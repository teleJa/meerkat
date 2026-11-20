import { expect, test } from "@playwright/test";

test("settings navigation selects and focuses each section", async ({ page }) => {
  await page.goto("/#/live/settings");
  const navigation = page.getByRole("navigation", { name: "设置分区" });
  for (const [label, section, target] of [
    ["网络", "network", "settings-network"],
    ["通知", "notifications", "settings-notifications"],
    ["关于 Meerkat", "about", "about-meerkat-title"],
  ]) {
    const button = navigation.getByRole("button", { name: label, exact: true });
    await button.click();
    await expect(page).toHaveURL(new RegExp(`#/live/settings\\?section=${section}$`));
    await expect(button).toHaveAttribute("aria-current", "location");
    await expect(page.locator(`#${target}`)).toBeFocused();
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
