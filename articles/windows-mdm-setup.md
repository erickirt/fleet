# Windows MDM setup

![Windows MDM setup](../website/assets/images/articles/windows-mdm-fleet-1600x900@2x.png)

To control OS settings, updates, and more on Windows hosts follow the manual enrollment instructions.

To use automatic enrollment (aka zero-touch) features on Windows, follow instructions to connect Fleet to Microsoft Entra ID. You can further customize zero-touch with Windows Autopilot.

To migrate Windows hosts from your current MDM solution to Fleet, follow the [Automatic Windows MDM migration](#automatic-windows-mdm-migration) instructions.

## Turn on Windows MDM

### Step 1: Generate your certificate and key

Fleet uses a certificate and key pair to authenticate and manage interactions between the Fleet server and a Windows host.

How to generate a certificate and key:

1. With [OpenSSL](https://www.openssl.org/) installed, open your Terminal (macOS) or PowerShell (Windows) and run the following command to create a key: `openssl genrsa --traditional -out fleet-mdm-win-wstep.key 4096`.

2. Create a certificate: `openssl req -x509 -new -nodes -key fleet-mdm-win-wstep.key -sha256 -days 3652 -out fleet-mdm-win-wstep.crt -subj '/CN=Fleet Root CA/C=US/O=Fleet.'`.

> Note: The default `openssl` binary installed on macOS is actually `LibreSSL`, which doesn't support the `--traditional` flag. To successfully generate these files, make sure you're using `OpenSSL` and not `LibreSSL`. You can check what your `openssl` command points to by running `openssl version`.

### Step 2: Configure Fleet with your certificate and key

In your Fleet server configuration, set the contents of the certificate and key in the following environment variables:

> Note: Any environment variable that ends in `_BYTES` expects the file's actual content to be passed in, not a path to the file. If you want to pass in a file path, remove the `_BYTES` suffix from the environment variable.

- [FLEET_MDM_WINDOWS_WSTEP_IDENTITY_CERT_BYTES](https://fleetdm.com/docs/deploying/configuration#mdm-windows-wstep-identity-cert-bytes)
- [FLEET_MDM_WINDOWS_WSTEP_IDENTITY_KEY_BYTES](https://fleetdm.com/docs/deploying/configuration#mdm-windows-wstep-identity-key-bytes)

Restart the Fleet server.

### Step 3: Turn on Windows MDM

1. Head to the **Settings > Integrations > Mobile device management (MDM)** page.

2. Next to **Turn on Windows MDM** select **Turn on** to navigate to the **Manage Windows MDM** page.

3. Select **Turn on**.

## Manual enrollment

With Windows MDM turned on, enroll a Windows host to Fleet by installing [Fleet's agent (fleetd)](https://fleetdm.com/docs/using-fleet/enroll-hosts).

> Windows [tamper protection](https://learn.microsoft.com/en-us/defender-endpoint/prevent-changes-to-security-settings-with-tamper-protection) is disabled on a host when MDM is turned on.

## Automatic enrollment

> Available in Fleet Premium

To automatically enroll Windows workstations when they’re first unboxed and set up by your end users, we will connect Fleet to Microsoft Entra ID.

After you connect Fleet to Microsoft Entra ID, you can customize the Windows setup experience with [Windows Autopilot](https://learn.microsoft.com/en-us/autopilot/windows-autopilot).

In order to connect Fleet to Microsoft Entra ID, the IT admin (you) needs a Microsoft Enterprise Mobility + Security E3 license. Each end user who automatically enrolls needs at least a [Microsoft Entra P1 license](https://www.microsoft.com/en-us/security/business/microsoft-entra-pricing). If they already have an [E3 or E5 license](https://www.microsoft.com/en-us/microsoft-365/enterprise/microsoft365-plans-and-pricing) then you're good to go.

### Step 1: Buy Microsoft licenses

1. Sign in to [Microsoft 365 admin center](https://admin.microsoft.com/).

2. In the left-side bar select **Marketplace**.

3. On the **Marketplace** page, select **All products** and in the search bar below **All products** enter "Enterprise Mobility + Security E3".

4. Find **Enterprise Mobility + Security E3** and select **Details**

5. On the **Enterprise Mobility + Security E3** page, select **Buy** and follow instructions to purchase the license. 

6. Find and buy a license.

7. Sign in to [Microsoft Entra ID portal](https://portal.azure.com).

8. At the top of the page search "Users" and select **Users**.

9. Select or create a test user and select **Licenses**.

10. Select **+ Assignments** and assign yourself the **Enterprise Mobility + Security E3**. Assign the test user the Intune licnese.

### Step 2: Connect Fleet to Microsoft Entra ID

For instructions on how to connect Fleet to Microsoft Entra ID, in the Fleet UI, select the avatar on the right side of the top navigation and select **Settings > Integrations > Mobile device management (MDM)**. Then, next to **Windows automatic enrollment** select **Details**.

### Step 3: Test automatic enrollment

Testing automatic enrollment requires creating a test user in Microsoft Entra ID and a freshly wiped or new Windows workstation.

1. Sign in to [Microsoft Entra ID portal](https://portal.azure.com).

2. At the top of the page search "Users" and select **Users**.

3. Select **+ New user > Create new user**, fill out the details for your test user, and select **Review + Create > Create**.

4. Go back to **Users** and refresh the page to confirm that your test user was created.

5. Open your Windows workstation and follow the setup steps. When you reach the **How would you like to set up?** screen, select **Set up for an organization**. If your workstations has Windows 11, select **Set up for work or school**.

6. Sign in with your test user's credentials and finish the setup steps.

7. When you reach the desktop on your Windows workstation, confirm that your workstation was automatically enrolled to Fleet by selecting the carrot (^) in your taskbar and then selecting the Fleet icon. This will navigate you to this workstation's **My device** page.

8. On the **My device** page, below **My device** confirm that your workstation has a **Status** of "Online."

## Windows Autopilot

### Step 1: Create an Autopilot profile

1. Sign in to [Microsoft Intune](https://endpoint.microsoft.com/) using the Intune admin user from step 1.

2. In the left-side bar select **Devices > Enroll devices**. Under **Windows Autopilot Deployment Program** select **Deployment Profiles** to navigate to the **Windows Autopilot deployment profiles** page.

3. Select **+ Create profile > Windows PC** and follow steps to create an Autopilot profile. On the **Assignments** step, select **+ Add all devices**.

### Step 2: Register a test workstation

1. Open your test workstation and follow these [Microsoft instructions](https://learn.microsoft.com/en-us/autopilot/add-devices#desktop-hash-export) to export your workstations's device hash as a CSV. The CSV should look something like `DeviceHash_DESKTOP-2V08FUI.csv`

2. In Intune, in the left-side bar, select **Devices > Enroll devices**. Under **Windows Autopilot Deployment Program** select **Devices** to navigate to the **Windows Autopilot devices** page.

3. Select **Import** and import your CSV.

4. After Intune finishes the import, refresh the **Windows Autopilot devices** page several times to confirm that your workstation is registered with Autopilot.

### Step 3: Upload your organization's logo

1. Navigate to [Microsoft Entra ID portal](https://portal.azure.com).

2. At the top of the page, search for "Microsoft Entra ID", select **Microsoft Entra ID**, and then select **Company branding**.

3. On the **Company Branding** page, select **Configure** or **Edit** under **Default sign-in experience**.

4. Select the **Sign-in form** tab and upload your logo to the **Square logo (light theme)** and **Square logo (dark theme)** fields.

5. In the bottom bar, select **Review + Save** and then **Save**.

### Step 4: Test Autopilot

1. Wipe your test workstation.

2. After it's been wiped, open your workstation and follow the setup steps. At screen in which you're asked to sign in, you should see the title "Welcome to [your organziation]!" next to the logo you uploaded in step 4.


## Automatic Windows MDM migration

Fleet can automatically migrate your Windows hosts from another MDM solution to Fleet without end user interaction.

### Step 1: Set up Windows MDM in Fleet

Follow the [steps above](#manual-enrollment) to turn on Windows MDM in Fleet. 

### Step 2: Install Fleet's agent on the hosts

1. [Enroll](https://fleetdm.com/docs/using-fleet/enroll-hosts) the Windows hosts you want to migrate to Fleet.

2. Navigate to the **Hosts** tab in the main navigation bar and wait until your hosts are visible in the hosts list.

### Step 3: Enable automatic migration

1. Head back to the **Settings > Integrations > Mobile device management (MDM)** page.

2. Next to **Windows MDM turned on (servers excluded)** select **Edit** to navigate to the **Manage Windows MDM** page.

3. On the **Manage Windows MDM** page, select **Automatically migrate hosts connected to another MDM solution**. Click **Save** to save the change.

### Step 4: Monitor your hosts as they migrate to Fleet MDM

Once the automatic migration is enabled, Fleet sends a notification to each host to tell it to migrate. This process usually takes a few minutes at most.

You can [track migration progress in Fleet](https://fleetdm.com/guides/mdm-migration#check-migration-progress).

## Turn off Windows MDM

1. Turn off MDM for each host, by running [this script](https://github.com/fleetdm/fleet/blob/main/it-and-security/lib/windows/scripts/turn-off-mdm.ps1) on all your Windows hosts.
2. Head to **Settings > Integrations > MDM**.
3. In the **Mobile device management (MDM)** section, select **Edit** next to "Windows MDM turned on."
4. Switch **Windows MDM on** to **Windows MDM off** and select **Save**.

<meta name="articleTitle" value="Windows MDM setup">
<meta name="authorFullName" value="Noah Talerman">
<meta name="authorGitHubUsername" value="noahtalerman">
<meta name="category" value="guides">
<meta name="publishedOn" value="2023-10-23">
<meta name="articleImageUrl" value="../website/assets/images/articles/windows-mdm-fleet-1600x900@2x.png">
<meta name="description" value="Configuring Windows MDM in Fleet.">
