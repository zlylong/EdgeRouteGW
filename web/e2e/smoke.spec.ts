import { expect, test } from '@playwright/test'
import { readFileSync } from 'node:fs'

const pwFile = process.env.ERGW_E2E_PASSWORD_FILE ?? '.e2e-data/bootstrap_password.txt'
const bootstrap = readFileSync(pwFile, 'utf8').trim()
const newPassword = 'e2e-password-' + Date.now()

test.describe.serial('first run', () => {
  test('login with the bootstrap password forces a password change', async ({ page }) => {
    await page.goto('/')
    await expect(page).toHaveURL(/\/login/)
    await page.getByLabel('管理员密码').fill(bootstrap)
    await page.getByRole('button', { name: '登录' }).click()
    await expect(page).toHaveURL(/\/change-password/)
    const inputs = page.locator('input[type=password]')
    await inputs.nth(0).fill(bootstrap)
    await inputs.nth(1).fill(newPassword)
    await inputs.nth(2).fill(newPassword)
    await page.getByRole('button', { name: '保存' }).click()
    await expect(page).toHaveURL(/\/$/)
    await expect(page.getByRole('heading', { name: '总览' })).toBeVisible()
  })

  test('add a node, a rule set and rules, then trace a domain', async ({ page }) => {
    await page.goto('/login')
    await page.getByLabel('管理员密码').fill(newPassword)
    await page.getByRole('button', { name: '登录' }).click()
    await expect(page).toHaveURL(/\/$/)
    await page.goto('/nodes')
    await page.getByRole('button', { name: '添加节点' }).click()
    const dialog = page.getByRole('dialog')
    await dialog.getByPlaceholder('如 SG-01').fill('E2E-SG')
    await dialog.getByPlaceholder('域名或 IP').fill('sg.example.com')
    await dialog.locator('input[type=password]').fill('11111111-2222-3333-4444-555555555555')
    await dialog.getByPlaceholder('www.apple.com').fill('www.apple.com')
    await dialog.getByRole('button', { name: '保存' }).click()
    await expect(page.getByText('E2E-SG')).toBeVisible()

    await page.goto('/routing')
    await page.getByRole('button', { name: '新建规则集' }).first().click()
    await page.getByRole('dialog').getByPlaceholder('如 流媒体').fill('E2E')
    await page.getByRole('dialog').getByRole('button', { name: '创建' }).click()
    await page.getByRole('button', { name: '添加规则' }).click()
    await page.getByRole('dialog').locator('textarea').fill('**.youtube.com, **.google.com')
    await page.getByRole('dialog').getByRole('button', { name: '保存' }).click()
    await expect(page.getByText('**.youtube.com')).toBeVisible()
    await expect(page.getByText('**.google.com')).toBeVisible()

    await page.getByPlaceholder(/输入域名或 IP/).fill('music.youtube.com')
    await expect(page.getByText(/命中「E2E」/)).toBeVisible()
    await expect(page.getByText(/node-/).first()).toBeVisible()

    // The change-status pill shows the applied generation once the reconciler ran.
    await expect(page.getByRole('button', { name: '变更状态' })).toContainText(/gen \d+/)
  })

  test('logout returns to the login page and protects the API', async ({ page, request }) => {
    await page.goto('/login')
    await page.getByLabel('管理员密码').fill(newPassword)
    await page.getByRole('button', { name: '登录' }).click()
    await expect(page).toHaveURL(/\/$/)
    await page.getByRole('button', { name: '退出登录' }).click()
    await expect(page).toHaveURL(/\/login/)
    const res = await request.get('/api/v2/state')
    expect(res.status()).toBe(401)
  })
})
