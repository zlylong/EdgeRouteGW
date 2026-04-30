const { test, expect } = require('@playwright/test');

test.describe('ProxyGW Test Tools UI', () => {
    test.beforeEach(async ({ page }) => {
        // Assume basic login logic is already handled by auth or just bypass if possible
        // For this test, we navigate directly to the app
        await page.goto('http://localhost/ui/');
        await page.fill('input[type="password"]', 'admin');
        await page.click('button:has-text("登录系统")');
        
        // Switch to advanced mode if the tab is hidden
        const testTab = page.locator('a:has-text("诊断测试工具")');
        if (!await testTab.isVisible()) {
            await page.click('button:has-text("切换高级视图")');
        }
        await testTab.click();
    });

    test('should simulate routing trace', async ({ page }) => {
        const input = page.locator('input[placeholder*="测试域名"]');
        await input.fill('example.com');
        await page.click('button:has-text("追踪")');

        // Check for result container
        const result = page.locator('div:has-text("目标类型")');
        await expect(result).toBeVisible();
        await expect(result).toContainText('DOMAIN');
    });

    test('should run system health check', async ({ page }) => {
        // Health check usually runs automatically on tab entry, but we can trigger it
        await page.click('button:has-text("重新检测")');

        // Verify key components exist in results
        await expect(page.locator('span:has-text("Database")')).toBeVisible();
        await expect(page.locator('span:has-text("Xray")')).toBeVisible();
        await expect(page.locator('span:has-text("Mosdns")')).toBeVisible();
        
        // Check for OK status
        const okBadges = page.locator('span:has-text("OK")');
        const count = await okBadges.count();
        expect(count).toBeGreaterThan(0);
    });
});
