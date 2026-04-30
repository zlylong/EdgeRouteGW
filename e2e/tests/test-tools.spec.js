const { test, expect } = require('@playwright/test');

test.describe('ProxyGW Test Tools UI', () => {
    test.beforeEach(async ({ page }) => {
        // Assume basic login logic is already handled by auth or just bypass if possible
        // For this test, we navigate directly to the app
        await page.goto('http://127.0.0.1/ui/');
        
        // Inject token and ui_mode directly to bypass login and splash screen
        await page.evaluate(() => {
            localStorage.setItem('token', 'e2e-token');
            localStorage.setItem('ui_mode', 'advanced');
        });
        
        // Reload to apply localStorage
        await page.goto('http://127.0.0.1/ui/');
        
        // Wait for potential loading overlay to disappear
        await page.waitForSelector('div.bg-opacity-90', { state: 'hidden', timeout: 5000 }).catch(() => {});
        
        // Switch to advanced mode if the tab is hidden
        const testTab = page.locator('a:has-text("诊断测试工具")');
        if (!await testTab.isVisible()) {
            const advBtn = page.locator('button:has-text("切换高级视图")');
            if (await advBtn.isVisible()) {
                await advBtn.click();
            }
        }
        await testTab.click();
    });

    test('should simulate routing trace', async ({ page }) => {
        const input = page.locator('input[placeholder*="测试域名"]');
        await input.fill('example.com');
        await page.click('button:has-text("追踪")');

        // Check for result container
        const result = page.locator('div:has-text("目标类型")').last();
        await expect(result).toBeVisible({ timeout: 10000 });
        await expect(result).toContainText('domain', { ignoreCase: true });
    });

    test('should run system health check', async ({ page }) => {
        // Health check usually runs automatically on tab entry, but we can trigger it
        const refreshBtn = page.locator('button:has-text("重新检测")');
        await expect(refreshBtn).toBeVisible();
        await refreshBtn.click();

        // Verify key components exist in results
        await expect(page.locator('span:has-text("Database")').last()).toBeVisible({ timeout: 10000 });
        await expect(page.locator('span:has-text("Xray")').last()).toBeVisible();
        await expect(page.locator('span:has-text("Mosdns")').last()).toBeVisible();
        
        // Check for OK status
        const okBadges = page.locator('span:has-text("OK")');
        const count = await okBadges.count();
        expect(count).toBeGreaterThan(0);
    });
});
