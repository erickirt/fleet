import React from "react";

import { ITeam } from "interfaces/team";
import { IUserFormErrors, UserRole } from "interfaces/user";
import Modal from "components/Modal";
import UserForm from "../UserForm";
import { IUserFormData } from "../UserForm/UserForm";

interface IEditUserModalProps {
  onCancel: () => void;
  onSubmit: (formData: IUserFormData) => void;
  defaultName?: string;
  defaultEmail?: string;
  defaultGlobalRole?: UserRole | null;
  defaultTeamRole?: UserRole;
  defaultTeams?: ITeam[];
  availableTeams: ITeam[];
  currentTeam?: ITeam;
  isPremiumTier: boolean;
  smtpConfigured: boolean;
  sesConfigured: boolean;
  canUseSso: boolean; // corresponds to whether SSO is enabled for the organization
  isSsoEnabled?: boolean; // corresponds to whether SSO is enabled for the individual user
  isMfaEnabled?: boolean; // corresponds to whether MFA is enabled for the individual user
  isApiOnly?: boolean;
  editUserErrors: IUserFormErrors;
  isModifiedByGlobalAdmin?: boolean | false;
  isInvitePending?: boolean;
  isUpdatingUsers: boolean;
}

const baseClass = "edit-user-modal";

const EditUserModal = ({
  onCancel,
  onSubmit,
  defaultName,
  defaultEmail,
  defaultGlobalRole,
  defaultTeamRole,
  defaultTeams,
  availableTeams,
  isPremiumTier,
  smtpConfigured,
  sesConfigured,
  canUseSso,
  isSsoEnabled,
  isMfaEnabled,
  isApiOnly,
  currentTeam,
  editUserErrors,
  isModifiedByGlobalAdmin,
  isInvitePending,
  isUpdatingUsers,
}: IEditUserModalProps): JSX.Element => {
  return (
    <Modal
      title="Edit user"
      onExit={onCancel}
      className={`${baseClass}__edit-user-modal`}
    >
      <UserForm
        ancestorErrors={editUserErrors}
        defaultName={defaultName}
        defaultEmail={defaultEmail}
        defaultGlobalRole={defaultGlobalRole}
        defaultTeamRole={defaultTeamRole}
        defaultTeams={defaultTeams}
        onCancel={onCancel}
        onSubmit={onSubmit}
        availableTeams={availableTeams}
        isPremiumTier={isPremiumTier}
        smtpConfigured={smtpConfigured}
        sesConfigured={sesConfigured}
        canUseSso={canUseSso}
        isSsoEnabled={isSsoEnabled}
        isMfaEnabled={isMfaEnabled}
        isApiOnly={isApiOnly}
        isModifiedByGlobalAdmin={isModifiedByGlobalAdmin}
        isInvitePending={isInvitePending}
        currentTeam={currentTeam}
        isUpdatingUsers={isUpdatingUsers}
      />
    </Modal>
  );
};

export default EditUserModal;
