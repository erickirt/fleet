module.exports = {


  friendlyName: 'Deliver contact form message',


  description: 'Deliver a contact form message to the appropriate internal channel(s).',


  inputs: {

    emailAddress: {
      required: true,
      type: 'string',
      description: 'A return email address where we can respond.',
      example: 'hermione@hogwarts.edu'
    },

    firstName: {
      required: true,
      type: 'string',
      description: 'The first name of the human sending this message.',
      example: 'Emma'
    },

    lastName: {
      required: true,
      type: 'string',
      description: 'The last name of the human sending this message.',
      example: 'Watson'
    },

    message: {
      type: 'string',
      required: true,
      description: 'The custom message, in plain text.'
    }

  },


  exits: {

    success: {
      description: 'The message was sent successfully.'
    }

  },


  fn: async function({emailAddress, firstName, lastName, message}) {


    let userHasPremiumSubscription = false;
    let thisSubscription;
    if(this.req.me){
      thisSubscription = await Subscription.findOne({user: this.req.me.id});
      if(thisSubscription) {
        userHasPremiumSubscription = true;
      }
    }

    if (!sails.config.custom.slackWebhookUrlForContactForm) {
      throw new Error(
        'Message not delivered: slackWebhookUrlForContactForm needs to be configured in sails.config.custom. Here\'s the undelivered message: ' +
        `Name: ${firstName + ' ' + lastName}, Email: ${emailAddress}, Message: ${message ? message : 'No message.'}`
      );
    }

    let subject = 'New contact form message';
    if(userHasPremiumSubscription) {
      // If the user has a Fleet Premium subscription, prepend the message with details about their subscription.
      let subscriptionDetails =`
Fleet Premium subscription details:
- Fleet Premium subscriber since: ${new Date(thisSubscription.createdAt).toISOString().split('T')[0]}
- Next billing date: ${new Date(thisSubscription.nextBillingAt).toISOString().split('T')[0]}
- Host count: ${thisSubscription.numberOfHosts}
- Organization: ${this.req.me.organization}
-----

      `;
      message = subscriptionDetails + message;
      subject = 'New Fleet Premium customer message';
    }

    await sails.helpers.sendTemplateEmail.with({
      to: sails.config.custom.fromEmailAddress,
      replyTo: {
        name: firstName + ' '+ lastName,
        emailAddress: emailAddress,
      },
      subject,
      layout: false,
      template: 'email-contact-form',
      templateData: {
        emailAddress,
        firstName,
        lastName,
        message,
      },
    });


    sails.helpers.salesforce.updateOrCreateContactAndAccount.with({
      emailAddress: emailAddress,
      firstName: firstName,
      lastName: lastName,
      contactSource: 'Website - Contact forms',
      description: `Sent a contact form message: ${message}`,
    }).exec((err)=>{// Use .exec() to run the salesforce helpers in the background.
      if(err) {
        sails.log.warn(`Background task failed: When a user submitted a contact form message, a lead/contact could not be updated in the CRM for this email address: ${emailAddress}.`, err);
      }
      return;
    });

  }

};
